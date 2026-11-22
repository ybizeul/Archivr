import { useEffect, useRef, useState } from 'react'
import { ActionIcon, Autocomplete, Box, Button, Chip, Group, Loader, Modal, ScrollArea, Select, Stack, TagsInput, Text, TextInput, Title, Tooltip, UnstyledButton } from '@mantine/core'
import { useMediaQuery } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { IconChevronDown, IconChevronLeft, IconChevronRight, IconChevronUp } from '@tabler/icons-react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  useCorrespondents,
  useInboxQueue,
  useDeleteDocument,
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
  const isMobile = useMediaQuery('(max-width: 767px)', undefined, { getInitialValueInEffect: false }) ?? false
  const queue = useInboxQueue()
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = queue
  const patch = usePatchDocument()
  const remove = useDeleteDocument()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [command, setCommand] = useState<Command | null>(null)

  // Load the rest of the inbox in the background, one page at a time, until the queue is complete.
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage])

  // Tag filter: a document stays in the view only if it carries every selected tag (AND).
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  const [bulkValues, setBulkValues] = useState<BulkChanges | null>(null)
  const [bulkRunning, setBulkRunning] = useState(false)
  const folderList = useFolders()
  const typeList = useTypes()

  const allItems = queue.items
  const tagCounts = new Map<string, number>()
  for (const d of allItems) for (const t of d.tags) tagCounts.set(t, (tagCounts.get(t) ?? 0) + 1)
  const tagOptions = [...tagCounts.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  // A tag drops out of the filter once no inbox document carries it any more.
  const activeTags = selectedTags.filter((t) => tagCounts.has(t))
  const filtering = activeTags.length > 0
  const items = filtering ? allItems.filter((d) => activeTags.every((t) => d.tags.includes(t))) : allItems
  const total = filtering ? items.length : queue.total
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

  if (allItems.length === 0 && !filed) {
    return <EmptyInbox onUploaded={() => undefined} />
  }

  const filterBar = (
    <TagFilterBar
      options={tagOptions}
      value={activeTags}
      onChange={setSelectedTags}
      shown={items.length}
      total={allItems.length}
    />
  )

  if (!viewDoc) {
    return (
      <Box>
        {filterBar}
        <Text c="dimmed" p="xl">
          No inbox document carries all of these tags.
        </Text>
      </Box>
    )
  }

  const fileAll = async (changes: BulkChanges) => {
    const targets = items
    let done = 0
    let failed = 0
    setBulkRunning(true)
    for (const d of targets) {
      // Only the fields changed on the card are sent. The title is never sent: every document keeps its own.
      // Added tags are merged into the ones each document already has.
      const body: DocumentPatch = { folder_id: changes.folderId }
      if (changes.documentDate) body.document_date = changes.documentDate
      if (changes.typeId) body.type_id = changes.typeId
      if (changes.correspondent) body.correspondent = changes.correspondent
      if (changes.addTags.length > 0) body.tags = [...new Set([...d.tags, ...changes.addTags])]
      try {
        await patch.mutateAsync({ id: d.id, patch: body })
        done++
      } catch {
        failed++
      }
    }
    setBulkRunning(false)
    setBulkValues(null)
    notifications.show({
      color: failed ? 'red' : 'prussian',
      title: failed ? `${failed} could not be filed` : undefined,
      message: `Filed ${done} document${done === 1 ? '' : 's'}`,
    })
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
      onDelete={() => setConfirmDelete(true)}
      bulkCount={items.length}
      bulkBusy={bulkRunning || queue.hasNextPage}
      onFileAll={setBulkValues}
    />
  )

  const bulkModal = (
    <Modal
      opened={bulkValues !== null}
      onClose={() => !bulkRunning && setBulkValues(null)}
      title={`File ${items.length} document${items.length === 1 ? '' : 's'}?`}
      centered
    >
      <Stack>
        <Text size="sm">
          {filtering ? `Every document tagged ${activeTags.join(' + ')}` : 'Every document in the inbox'} will be filed
          with the folder and the fields you changed below. Each keeps its own title, and added tags are merged with its existing ones.
        </Text>
        {bulkValues && (
          <Stack gap={2}>
            <Text size="sm">
              <b>Folder:</b> {folderLabel(folderList.data?.find((f) => f.id === bulkValues.folderId)?.path ?? '')}
            </Text>
            {bulkValues.documentDate && (
              <Text size="sm">
                <b>Date:</b> {formatDate(bulkValues.documentDate)}
              </Text>
            )}
            {bulkValues.typeId && (
              <Text size="sm">
                <b>Type:</b> {typeList.data?.find((t) => t.id === bulkValues.typeId)?.name}
              </Text>
            )}
            {bulkValues.correspondent && (
              <Text size="sm">
                <b>Correspondent:</b> {bulkValues.correspondent}
              </Text>
            )}
            {bulkValues.addTags.length > 0 && (
              <Text size="sm">
                <b>Add tags:</b> {bulkValues.addTags.join(', ')}
              </Text>
            )}
          </Stack>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setBulkValues(null)} disabled={bulkRunning}>
            Cancel
          </Button>
          <Button loading={bulkRunning} onClick={() => bulkValues && void fileAll(bulkValues)}>
            File all
          </Button>
        </Group>
      </Stack>
    </Modal>
  )

  const deleteModal = (
    <Modal opened={confirmDelete} onClose={() => setConfirmDelete(false)} title={`Delete No. ${viewDoc.id}?`} centered>
      <Stack>
        <Text size="sm">This removes the file and its search entry. The number is not reused. This cannot be undone.</Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setConfirmDelete(false)}>
            Keep it
          </Button>
          <Button
            color="oxide"
            loading={remove.isPending}
            onClick={() => {
              const id = viewDoc.id
              remove.mutate(id, {
                onSuccess: () => {
                  notifications.show({ color: 'prussian', message: `Deleted No. ${id}` })
                  setConfirmDelete(false)
                  if (nextAfterCurrent) setParams({ doc: String(nextAfterCurrent) }, { replace: true })
                  else setParams({}, { replace: true })
                },
                onError: (err) => notifications.show({ color: 'red', title: "Couldn't delete", message: errorMessage(err) }),
              })
            }}
          >
            Delete document
          </Button>
        </Group>
      </Stack>
    </Modal>
  )

  if (isMobile) {
    return (
      <Box className="ax-triage ax-triage-mobile">
        {filterBar}
        <QueueStepper
          items={items}
          index={index}
          total={total}
          selectedId={viewDoc.id}
          busy={filed !== null}
          onSelect={select}
          onMove={move}
        />
        <Sheet {...sheetProps} />
        <Box className="ax-sheet-panel">{card}</Box>
        {deleteModal}
        {bulkModal}
      </Box>
    )
  }

  return (
    <Box className="ax-triage">
      {filterBar}
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
      {deleteModal}
      {bulkModal}
    </Box>
  )
}

/** Top bar: selectable tag pills. A document must carry every selected tag to stay in view. */
function TagFilterBar({
  options,
  value,
  onChange,
  shown,
  total,
}: {
  options: [string, number][]
  value: string[]
  onChange: (tags: string[]) => void
  shown: number
  total: number
}) {
  return (
    <Group className="ax-filterbar" gap="xs" wrap="nowrap" align="flex-start">
      <Text className="ax-mono ax-accent" size="xs" pt={5} style={{ whiteSpace: 'nowrap' }}>
        Tags (all of){value.length > 0 ? ` · ${shown} of ${total}` : ''}
      </Text>
      {options.length === 0 ? (
        <Text size="xs" c="dimmed" pt={5}>
          No tagged documents in the inbox.
        </Text>
      ) : (
        <Chip.Group multiple value={value} onChange={onChange}>
          <Group gap={6} style={{ flex: 1, minWidth: 0 }}>
            {options.map(([tag, count]) => (
              <Chip key={tag} value={tag} size="xs" radius="xl" variant="outline">
                {tag} · {count}
              </Chip>
            ))}
          </Group>
        </Chip.Group>
      )}
      {value.length > 0 && (
        <Button variant="subtle" color="gray" size="compact-xs" onClick={() => onChange([])}>
          Clear
        </Button>
      )}
    </Group>
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
      <DocViewer key={doc.id} id={doc.id} mime={doc.mime} title={doc.title} rotation={doc.rotation} />
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

/** Phone queue control: step through the inbox with arrows, or jump to any document from the list. */
function QueueStepper({
  items,
  index,
  total,
  selectedId,
  busy,
  onSelect,
  onMove,
}: {
  items: Document[]
  index: number
  total: number
  selectedId: number
  busy: boolean
  onSelect: (id: number) => void
  onMove: (delta: number) => void
}) {
  return (
    <Group className="ax-queue-strip" gap={6} wrap="nowrap" align="flex-end" aria-label="Queue">
      <ActionIcon variant="default" size="lg" aria-label="Previous document" onClick={() => onMove(-1)} disabled={index === 0 || busy}>
        <IconChevronLeft size={18} stroke={1.6} />
      </ActionIcon>
      <Select
        className="ax-queue-select"
        aria-label={`Queue, ${Math.max(1, index + 1)} of ${total}`}
        data={items.map((d) => ({ value: String(d.id), label: `No. ${d.id} · ${d.title}` }))}
        value={String(selectedId)}
        onChange={(v) => v && onSelect(Number(v))}
        allowDeselect={false}
        disabled={busy}
        size="sm"
        style={{ flex: 1, minWidth: 0 }}
      />
      <ActionIcon variant="default" size="lg" aria-label="Next document" onClick={() => onMove(1)} disabled={index >= items.length - 1 || busy}>
        <IconChevronRight size={18} stroke={1.6} />
      </ActionIcon>
    </Group>
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

/** What File all applies to every document in the view. Undefined means the card left that field alone. */
interface BulkChanges {
  folderId: number
  documentDate?: string
  typeId?: number
  correspondent?: string
  addTags: string[]
}

function FilingCard({
  doc,
  disabled,
  command,
  onSave,
  onFile,
  onDelete,
  bulkCount,
  bulkBusy,
  onFileAll,
}: {
  doc: Document
  disabled: boolean
  command: Command | null
  onSave: (v: FilingValues) => void
  onFile: (v: FilingValues) => void
  onDelete: () => void
  bulkCount: number
  bulkBusy: boolean
  onFileAll: (v: BulkChanges) => void
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

  // The title belongs to each document, so only the folder is required here. Only the fields the user
  // changed on this card are passed on; anything left as the viewed document has it is not applied to the others.
  const submitFileAll = () => {
    setAttempted(true)
    const v = values()
    if (!v.folderId) return
    onFileAll({
      folderId: v.folderId,
      documentDate: v.date !== (doc.document_date ?? '') ? v.date : undefined,
      typeId: v.typeId !== doc.type_id ? (v.typeId ?? undefined) : undefined,
      correspondent: v.correspondent !== doc.correspondent ? v.correspondent : undefined,
      // Tags already on the viewed document are not pushed to the others; only the newly added ones are.
      addTags: v.tags.filter((t) => !doc.tags.includes(t)),
    })
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
        <Button
          fullWidth
          variant="default"
          onClick={submitFileAll}
          disabled={disabled || bulkBusy || bulkCount === 0}
          title="Apply this folder and these details to every document in the current view. Each keeps its own title."
        >
          File all ({bulkCount})
        </Button>
        <Button fullWidth variant="subtle" color="gray" onClick={() => onSave(values())} disabled={disabled} size="xs">
          Save details
        </Button>
        <Button fullWidth variant="subtle" color="oxide" onClick={onDelete} disabled={disabled} size="xs">
          Delete
        </Button>
      </Stack>
    </Stack>
  )
}
