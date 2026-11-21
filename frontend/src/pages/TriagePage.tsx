import { useEffect, useRef, useState } from 'react'
import { ActionIcon, Autocomplete, Box, Button, Group, Loader, ScrollArea, Select, Stack, TagsInput, Text, TextInput, Title, Tooltip, UnstyledButton } from '@mantine/core'
import { useMediaQuery } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { IconChevronDown, IconChevronUp } from '@tabler/icons-react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  useCorrespondents,
  useDocuments,
  useFolders,
  useSuggestions,
  useTags,
  useTypes,
  usePatchDocument,
} from '../api/queries'
import type { Document, DocumentPatch } from '../api/types'
import { DateField } from '../components/DateField'
import { DocViewer } from '../components/DocViewer'
import { SheetBar } from '../components/SheetBar'
import { Stamp } from '../components/Stamp'
import { UploadButton } from '../components/UploadButton'
import { INBOX_ID } from '../lib/constants'
import { NewFolder } from '../components/NewFolder'
import { accession, errorMessage, folderLabel, formatDate, todayISO } from '../lib/format'

/** A keyboard command for the filing card. The timestamp makes a repeated key press count again. */
interface Command {
  kind: 'file' | 'title' | 'folder'
  at: number
}

export function TriagePage() {
  const [params, setParams] = useSearchParams()
  const isMobile = useMediaQuery('(max-width: 767px)') ?? false
  const queue = useDocuments({ inbox: true, sort: 'oldest', limit: 200 })
  const patch = usePatchDocument()
  const [command, setCommand] = useState<Command | null>(null)

  const items = queue.data?.items ?? []
  const total = queue.data?.total ?? 0
  const selectedId = Number(params.get('doc')) || 0
  const index = Math.max(0, items.findIndex((d) => d.id === selectedId))
  const current: Document | undefined = items[index]

  // A filed document stays on the light table for the length of the stamp, then the next one comes up.
  const [filed, setFiled] = useState<{ doc: Document; run: number; next: number | null } | null>(null)
  const viewDoc = filed?.doc ?? current

  const select = (id: number) => setParams({ doc: String(id) }, { replace: true })

  const move = (delta: number) => {
    if (filed) return
    const next = items[index + delta]
    if (next) select(next.id)
  }

  useEffect(() => {
    if (!filed) return
    const t = window.setTimeout(() => {
      setFiled(null)
      if (filed.next) setParams({ doc: String(filed.next) }, { replace: true })
    }, 3000)
    return () => window.clearTimeout(t)
    // setParams is stable for the life of the page; the timer belongs to this filing only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filed])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const target = e.target as HTMLElement | null
      if (target?.closest('input, textarea, select, [contenteditable="true"]')) return
      switch (e.key.toLowerCase()) {
        case 'j':
          move(1)
          break
        case 'k':
          move(-1)
          break
        case 'f':
          setCommand({ kind: 'file', at: Date.now() })
          break
        case 't':
          setCommand({ kind: 'title', at: Date.now() })
          break
        case 's':
          setCommand({ kind: 'folder', at: Date.now() })
          break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // move depends on the queue position, so it is rebuilt when that changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index, items.length, filed])

  if (queue.isPending) {
    return (
      <Box p="xl">
        <Loader size="sm" />
      </Box>
    )
  }

  if (!viewDoc) {
    return <EmptyInbox onUploaded={() => undefined} />
  }

  const fileDocument = async (values: FilingValues, mode: 'save' | 'file', next: number | null) => {
    const body: DocumentPatch = {
      title: values.title,
      document_date: values.date,
      type_id: values.typeId ?? 0,
      correspondent: values.correspondent,
      tags: values.tags,
    }
    if (mode === 'file' && values.folderId) body.folder_id = values.folderId
    try {
      const updated = await patch.mutateAsync({ id: viewDoc.id, patch: body })
      if (mode === 'save') {
        notifications.show({ color: 'prussian', message: `Saved No. ${updated.id}` })
        return
      }
      setFiled({ doc: updated, run: (filed?.run ?? 0) + 1, next })
    } catch (err) {
      notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) })
    }
  }

  const nextAfterCurrent = items[index + 1]?.id ?? items[index - 1]?.id ?? null

  const sheetProps = {
    doc: viewDoc,
    filed: filed !== null,
    stampRun: filed?.run ?? 0,
  }

  const card = (
    <FilingCard
      key={viewDoc.id}
      doc={viewDoc}
      disabled={filed !== null || patch.isPending}
      command={command}
      onSave={(values) => void fileDocument(values, 'save', null)}
      onFile={(values) => void fileDocument(values, 'file', nextAfterCurrent)}
    />
  )

  if (isMobile) {
    return (
      <Box className="ax-triage ax-triage-mobile">
        <QueueStrip items={items} selectedId={viewDoc.id} onSelect={select} />
        <Sheet {...sheetProps} />
        <Box className="ax-sheet-panel">{card}</Box>
      </Box>
    )
  }

  return (
    <Box className="ax-triage">
      <Box className="ax-queue-col">
        <QueueList items={items} total={total} selectedId={viewDoc.id} onSelect={select} />
      </Box>
      <Box className="ax-sheet-col">
        <Group justify="space-between" mb="xs" wrap="nowrap">
          <Text className="ax-mono ax-accent" size="xs">
            {total > 0 ? `${Math.max(1, index + 1)} of ${total}` : ''}
          </Text>
          <Group gap={4} wrap="nowrap">
            <Tooltip label="Previous (K)">
              <ActionIcon variant="default" aria-label="Previous document" onClick={() => move(-1)} disabled={index === 0 || filed !== null}>
                <IconChevronUp size={16} stroke={1.6} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label="Next (J)">
              <ActionIcon variant="default" aria-label="Next document" onClick={() => move(1)} disabled={index >= items.length - 1 || filed !== null}>
                <IconChevronDown size={16} stroke={1.6} />
              </ActionIcon>
            </Tooltip>
          </Group>
        </Group>
        <Sheet {...sheetProps} />
      </Box>
      <Box className="ax-card-col">{card}</Box>
    </Box>
  )
}

function EmptyInbox({ onUploaded }: { onUploaded: () => void }) {
  void onUploaded
  return (
    <Stack className="ax-empty" gap="sm" align="flex-start" maw={520} p="xl">
      <Title order={2}>Inbox is clear.</Title>
      <Text c="dimmed">New scans and emails land here.</Text>
      <Group gap="sm">
        <UploadButton variant="filled" size="sm" />
        <Button component={Link} to="/library" variant="default" size="sm">
          Open the library
        </Button>
      </Group>
    </Stack>
  )
}

function Sheet({ doc, filed, stampRun }: { doc: Document; filed: boolean; stampRun: number }) {
  return (
    <Box className="ax-sheet" data-filed={filed || undefined}>
      <SheetBar id={doc.id} />
      <DocViewer key={doc.id} id={doc.id} mime={doc.mime} title={doc.title} />
      <Stamp run={stampRun} label={`Filed · ${filedDate()}`} />
    </Box>
  )
}

function filedDate(): string {
  return formatDate(todayISO())
}

function QueueList({
  items,
  total,
  selectedId,
  onSelect,
}: {
  items: Document[]
  total: number
  selectedId: number
  onSelect: (id: number) => void
}) {
  return (
    <>
      <Text className="ax-queue-head ax-mono">Queue · {total}</Text>
      <ScrollArea className="ax-queue-scroll" type="auto">
        <Stack gap={6}>
          {items.map((d) => (
            <QueueItem key={d.id} doc={d} selected={d.id === selectedId} onSelect={onSelect} />
          ))}
        </Stack>
      </ScrollArea>
    </>
  )
}

function QueueStrip({ items, selectedId, onSelect }: { items: Document[]; selectedId: number; onSelect: (id: number) => void }) {
  return (
    <ScrollArea type="auto" className="ax-queue-strip" aria-label="Queue">
      <Group gap={6} wrap="nowrap">
        {items.map((d) => (
          <Tooltip key={d.id} label={`No. ${d.id} · ${d.title}`}>
            <UnstyledButton
              className={`ax-strip-item ax-mono${d.id === selectedId ? ' selected' : ''}`}
              onClick={() => onSelect(d.id)}
              aria-current={d.id === selectedId ? 'true' : undefined}
              aria-label={`No. ${d.id}, ${d.title}`}
            >
              {d.id}
            </UnstyledButton>
          </Tooltip>
        ))}
      </Group>
    </ScrollArea>
  )
}

function QueueItem({ doc, selected, onSelect }: { doc: Document; selected: boolean; onSelect: (id: number) => void }) {
  return (
    <UnstyledButton
      className={`ax-queue-item${selected ? ' selected' : ''}`}
      onClick={() => onSelect(doc.id)}
      aria-current={selected ? 'true' : undefined}
      aria-label={`No. ${doc.id}, ${doc.title}`}
    >
      <div className="ax-thumb">
        <img
          src={`/api/v1/documents/${doc.id}/thumbnail`}
          alt=""
          loading="lazy"
          onError={(e) => {
            e.currentTarget.style.visibility = 'hidden'
          }}
        />
      </div>
      <div className="ax-mono ax-queue-no">
        {doc.id}
        {doc.ocr_status === 'pending' && (
          <Tooltip label="OCR pending">
            <span className="ax-ocr-dot pending" aria-label="OCR pending" />
          </Tooltip>
        )}
        {doc.ocr_status === 'failed' && (
          <Tooltip label={doc.ocr_error || 'OCR failed'}>
            <span className="ax-ocr-dot failed" aria-label="OCR failed" />
          </Tooltip>
        )}
      </div>
      <div className="ax-queue-title">{doc.title}</div>
    </UnstyledButton>
  )
}

interface FilingValues {
  title: string
  date: string
  typeId: number | null
  correspondent: string
  tags: string[]
  folderId: number | null
}

function FilingCard({
  doc,
  disabled,
  command,
  onSave,
  onFile,
}: {
  doc: Document
  disabled: boolean
  command: Command | null
  onSave: (v: FilingValues) => void
  onFile: (v: FilingValues) => void
}) {
  const folders = useFolders()
  const types = useTypes()
  const correspondents = useCorrespondents()
  const allTags = useTags()
  const suggestions = useSuggestions(doc.id)

  const [title, setTitle] = useState(doc.title)
  const [date, setDate] = useState(doc.document_date ?? '')
  const [typeId, setTypeId] = useState<string | null>(doc.type_id ? String(doc.type_id) : null)
  const [correspondent, setCorrespondent] = useState(doc.correspondent)
  const [tags, setTags] = useState<string[]>(doc.tags)
  const [folderId, setFolderId] = useState<string | null>(doc.folder_id !== INBOX_ID ? String(doc.folder_id) : null)
  const [attempted, setAttempted] = useState(false)

  const titleRef = useRef<HTMLInputElement>(null)
  const folderRef = useRef<HTMLInputElement>(null)

  const values = (): FilingValues => ({
    title: title.trim(),
    date,
    typeId: typeId ? Number(typeId) : null,
    correspondent: correspondent.trim(),
    tags,
    folderId: folderId ? Number(folderId) : null,
  })

  const submitFile = () => {
    setAttempted(true)
    const v = values()
    if (!v.title || !v.folderId) return
    onFile(v)
  }

  // A card mounted after an earlier command (for example, after J) must not replay it.
  const [mountedWith] = useState(command?.at)
  useEffect(() => {
    if (!command || command.at === mountedWith) return
    if (command.kind === 'title') titleRef.current?.focus()
    else if (command.kind === 'folder') folderRef.current?.focus()
    else if (!disabled) {
      // A keyboard command reacts to the form as it is now. Showing the field errors here is
      // the one place an effect sets state on purpose: the command came from outside the form.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      submitFile()
    }
    // Only a new command should trigger this; the form is read fresh inside submitFile.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [command])

  const titleError = attempted && !title.trim() ? 'Enter a title' : undefined
  const folderError = attempted && !folderId ? 'Choose a folder to file into' : undefined

  const folderData = (folders.data ?? [])
    .filter((f) => !f.is_system)
    .map((f) => ({ value: String(f.id), label: folderLabel(f.path) }))
  const typeData = (types.data ?? []).map((t) => ({ value: String(t.id), label: t.name }))
  const correspondentData = (correspondents.data ?? []).map((c) => c.name)
  const tagData = (allTags.data ?? []).map((t) => t.name)

  const dates = suggestions.data?.dates.slice(0, 5) ?? []
  const titles = suggestions.data?.titles.slice(0, 3) ?? []

  return (
    <Stack className="ax-card" gap={10}>
      <div className="ax-mono ax-accent ax-card-no">{accession(doc.id)}</div>

      <TextInput
        ref={titleRef}
        label="Title"
        withAsterisk
        value={title}
        onChange={(e) => setTitle(e.currentTarget.value)}
        error={titleError}
        disabled={disabled}
        size="sm"
        maxLength={200}
      />
      {titles.length > 0 && (
        <Group gap={4} className="ax-chips" aria-label="Title suggestions">
          {titles.map((t) => (
            <button key={t} type="button" className="ax-chip" onClick={() => setTitle(t)} disabled={disabled}>
              {t}
            </button>
          ))}
        </Group>
      )}

      <DateField label="Date" value={date} onValueChange={setDate} disabled={disabled} size="sm" />
      {dates.length > 0 && (
        <Group gap={4} className="ax-chips" aria-label="Date suggestions">
          {dates.map((d) => (
            <button key={d.value} type="button" className="ax-chip" onClick={() => setDate(d.value)} disabled={disabled}>
              {formatDate(d.value)}
            </button>
          ))}
        </Group>
      )}

      <Select
        label="Type"
        data={typeData}
        value={typeId}
        onChange={setTypeId}
        clearable
        searchable
        disabled={disabled}
        size="sm"
        placeholder="No type"
      />

      <Autocomplete
        label="Correspondent"
        data={correspondentData}
        value={correspondent}
        onChange={setCorrespondent}
        disabled={disabled}
        size="sm"
        maxLength={200}
        placeholder="Pick one, or type a new name"
      />

      <TagsInput
        label="Tags"
        data={tagData}
        value={tags}
        onChange={setTags}
        disabled={disabled}
        size="sm"
        placeholder="Add a tag"
        clearable
      />

      <div>
        <Group align="flex-end" gap="xs" wrap="nowrap">
          <Select
            ref={folderRef}
            label="Folder"
            withAsterisk
            data={folderData}
            value={folderId}
            onChange={setFolderId}
            error={folderError !== undefined}
            disabled={disabled}
            searchable
            size="sm"
            placeholder="Choose a folder"
            nothingFoundMessage="No folder yet. Use the New folder button."
            style={{ flex: 1 }}
          />
          <NewFolder compact disabled={disabled} onCreated={(f) => setFolderId(String(f.id))} />
        </Group>
        {folderError && (
          <Text size="xs" c="red" mt={4}>
            {folderError}
          </Text>
        )}
      </div>

      <Stack gap={6} mt="auto" className="ax-card-actions">
        <Button fullWidth onClick={submitFile} disabled={disabled} className="ax-file-btn">
          {disabled ? 'Filed' : 'File document'}
        </Button>
        <Button fullWidth variant="subtle" color="gray" onClick={() => onSave(values())} disabled={disabled} size="xs">
          Save details
        </Button>
      </Stack>
    </Stack>
  )
}
