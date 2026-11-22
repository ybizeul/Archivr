import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { Alert, Anchor, Autocomplete, Badge, Box, Button, CopyButton, Group, Loader, ScrollArea, Select, Stack, TagsInput, Text, TextInput, Title, ActionIcon, Tooltip } from '@mantine/core'
import { useMediaQuery } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { IconArrowLeft, IconCopy, IconCheck, IconShare } from '@tabler/icons-react'
import { useCorrespondents, useDocument, useFolders, usePatchDocument, useReOCR, useTags, useTypes } from '../api/queries'
import type { DocumentDetail } from '../api/types'
import { DateField } from '../components/DateField'
import { DeleteDocumentDialog } from '../components/DeleteDocumentDialog'
import { DocViewer } from '../components/DocViewer'
import { LibraryBrowser } from '../components/LibraryBrowser'
import { ShareDialog } from '../components/ShareDialog'
import { SheetBar } from '../components/SheetBar'
import { errorMessage, folderTitle, formatBytes, formatDate, formatDateTime, isTrash, OCR_LABEL, todayISO } from '../lib/format'
import { useLibraryLocation } from '../lib/libraryLocation'

/** A document opens beside the library's folder tree (except on phones), so the user keeps their place. */
export function DocumentPage() {
  const { id: rawId } = useParams()
  const id = Number(rawId)
  const doc = useDocument(Number.isFinite(id) && id > 0 ? id : undefined)
  const navigate = useNavigate()
  const libraryLocation = useLibraryLocation()
  const isMobile = useMediaQuery('(max-width: 767px)', undefined, { getInitialValueInEffect: false }) ?? false

  // The folder and tag the user came from stay highlighted in the tree.
  const remembered = new URLSearchParams(libraryLocation.split('?')[1] ?? '')
  const folder = Number(remembered.get('folder')) || undefined
  const tag = remembered.get('tag') ?? ''

  let content
  if (doc.isPending) {
    content = (
      <Box p="xl">
        <Loader size="sm" />
      </Box>
    )
  } else if (doc.isError || !doc.data) {
    content = (
      <Stack p="xl" maw={480}>
        <Title order={3}>No such document</Title>
        <Text c="dimmed">It may have been deleted. Check the number and try again.</Text>
        <Anchor component={Link} to={libraryLocation}>
          Back to the library
        </Anchor>
      </Stack>
    )
  } else {
    content = <DocumentView key={doc.data.id} doc={doc.data} libraryLocation={libraryLocation} />
  }

  if (isMobile) return content

  const goLibrary = (changes: { folder?: number; tag?: string }) => {
    const next = new URLSearchParams()
    if (changes.folder) next.set('folder', String(changes.folder))
    if (changes.tag) next.set('tag', changes.tag)
    const search = next.toString()
    navigate(`/library${search ? `?${search}` : ''}`)
  }

  return (
    <Box className="ax-library ax-library-doc">
      <Box className="ax-library-side">
        <LibraryBrowser
          folder={folder}
          tag={tag}
          onFolder={(f) => goLibrary({ folder: f })}
          onTag={(t) => goLibrary({ folder, tag: t })}
        />
      </Box>
      <Box className="ax-library-main">{content}</Box>
    </Box>
  )
}

function DocumentView({ doc, libraryLocation }: { doc: DocumentDetail; libraryLocation: string }) {
  const navigate = useNavigate()
  const folders = useFolders()
  const types = useTypes()
  const correspondents = useCorrespondents()
  const tags = useTags()
  const save = usePatchDocument()
  const reocr = useReOCR()

  const [title, setTitle] = useState(doc.title)
  const [date, setDate] = useState(doc.document_date ?? '')
  const [typeName, setTypeName] = useState(doc.type?.name ?? '')
  const [correspondent, setCorrespondent] = useState(doc.correspondent)
  const [tagList, setTagList] = useState<string[]>(doc.tags)
  const [validUntil, setValidUntil] = useState(doc.valid_until ?? '')
  const [folderId, setFolderId] = useState<string>(String(doc.folder_id))
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [sharing, setSharing] = useState(false)
  const [titleError, setTitleError] = useState<string | undefined>()

  const onSave = () => {
    if (!title.trim()) {
      setTitleError('Enter a title')
      return
    }
    setTitleError(undefined)
    save.mutate(
      {
        id: doc.id,
        patch: {
          title: title.trim(),
          document_date: date,
          type: typeName.trim(),
          correspondent: correspondent.trim(),
          tags: tagList,
          folder_id: Number(folderId),
          // Left out when unchanged, so taking an expired document out of Trash clears its date on the server.
          valid_until: validUntil !== (doc.valid_until ?? '') ? validUntil : undefined,
        },
      },
      {
        onSuccess: (saved) => {
          setValidUntil(saved.valid_until ?? '')
          notifications.show({ color: 'prussian', message: `Saved No. ${doc.id}` })
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
      },
    )
  }

  const folderData = (folders.data ?? []).map((f) => ({
    value: String(f.id),
    label: folderTitle(f),
  }))

  const trash = (folders.data ?? []).find(isTrash)
  const inTrash = trash?.id === doc.folder_id

  return (
    <Box className="ax-detail">
      <Group justify="space-between" mb="md" wrap="nowrap">
        <Anchor component={Link} to={libraryLocation} size="sm" className="ax-back">
          <Group gap={6} wrap="nowrap">
            <IconArrowLeft size={16} stroke={1.6} />
            Library
          </Group>
        </Anchor>
        <Group gap="xs" wrap="nowrap">
          <Button
            variant="default"
            size="xs"
            leftSection={<IconShare size={14} stroke={1.6} />}
            onClick={() => setSharing(true)}
          >
            Share
          </Button>
          <Button variant="subtle" color="oxide" size="xs" onClick={() => setConfirmDelete(true)}>
            {inTrash ? 'Delete permanently' : 'Delete'}
          </Button>
        </Group>
      </Group>

      <div className="ax-detail-grid">
        <Box className="ax-sheet ax-detail-sheet">
          <SheetBar id={doc.id} />
          <DocViewer key={doc.id} id={doc.id} mime={doc.mime} title={doc.title} rotation={doc.rotation} />
        </Box>

        <Stack gap="lg" className="ax-detail-side">
          <Box>
            <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
              Details
            </Text>
            <Stack gap="sm" mt="xs">
              <TextInput
                label="Title"
                withAsterisk
                value={title}
                onChange={(e) => setTitle(e.currentTarget.value)}
                error={titleError}
                maxLength={200}
              />
              <DateField label="Date" value={date} onValueChange={setDate} />
              <DateField
                label="Valid until"
                description={
                  validUntil && validUntil < todayISO()
                    ? 'This date has passed.'
                    : 'Optional. The document moves to Trash the day after.'
                }
                value={validUntil}
                onValueChange={setValidUntil}
              />
              <Autocomplete
                label="Type"
                data={(types.data ?? []).map((t) => t.name)}
                value={typeName}
                onChange={setTypeName}
                maxLength={80}
                placeholder="Pick one, or type a new type"
              />
              <Autocomplete
                label="Correspondent"
                data={(correspondents.data ?? []).map((c) => c.name)}
                value={correspondent}
                onChange={setCorrespondent}
                maxLength={200}
                placeholder="Pick one, or type a new name"
              />
              <TagsInput
                label="Tags"
                data={(tags.data ?? []).map((t) => t.name)}
                value={tagList}
                onChange={setTagList}
                clearable
                placeholder="Add a tag"
              />
              <Select
                label="Folder"
                data={folderData}
                value={folderId}
                onChange={(v) => v && setFolderId(v)}
                allowDeselect={false}
                searchable
              />
              {inTrash && (
                <Alert color="gray" variant="light">
                  {doc.valid_until
                    ? `Moved here because it was valid until ${formatDate(doc.valid_until)}. Choose another folder and save to restore it; the date is then cleared.`
                    : 'This document is in the Trash. Choose another folder and save to restore it.'}
                </Alert>
              )}
              <Group justify="flex-end">
                <Button onClick={onSave} loading={save.isPending}>
                  Save changes
                </Button>
              </Group>
            </Stack>
          </Box>

          <Box>
            <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
              File
            </Text>
            <dl className="ax-facts">
              <dt>Filename</dt>
              <dd className="ax-mono">{doc.filename}</dd>
              <dt>Size</dt>
              <dd>{formatBytes(doc.size)}</dd>
              <dt>Type</dt>
              <dd className="ax-mono">{doc.mime}</dd>
              <dt>Added</dt>
              <dd>{formatDateTime(doc.created_at)}</dd>
              <dt>Source</dt>
              <dd>{doc.source === 'upload' ? 'Upload' : doc.source === 'imap' ? 'Email' : 'Watch folder'}</dd>
              <dt>SHA-256</dt>
              <dd className="ax-mono ax-hash">
                {doc.sha256.slice(0, 16)}…
                <CopyButton value={doc.sha256}>
                  {({ copied, copy }) => (
                    <Tooltip label={copied ? 'Copied' : 'Copy full hash'}>
                      <ActionIcon variant="subtle" color="gray" size="sm" onClick={copy} aria-label="Copy full hash">
                        {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                      </ActionIcon>
                    </Tooltip>
                  )}
                </CopyButton>
              </dd>
              {doc.document_date && (
                <>
                  <dt>Document date</dt>
                  <dd>{formatDate(doc.document_date)}</dd>
                </>
              )}
            </dl>
          </Box>

          <Box>
            <Group justify="space-between" wrap="wrap" mb="xs">
              <Group gap="xs" wrap="nowrap">
                <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
                  Text (OCR)
                </Text>
                <Badge variant="outline" color={doc.ocr_status === 'failed' ? 'oxide' : 'gray'} size="sm" radius="sm">
                  {OCR_LABEL[doc.ocr_status]}
                </Badge>
              </Group>
              <Button
                variant="default"
                size="xs"
                onClick={() => reocr.mutate(doc.id)}
                loading={reocr.isPending}
                disabled={doc.ocr_status === 'pending'}
              >
                Run OCR again
              </Button>
            </Group>

            {doc.ocr_status === 'failed' && (
              <Alert color="oxide" variant="light" mb="xs" title="OCR failed">
                {doc.ocr_error || 'The file could not be read.'}
              </Alert>
            )}
            {doc.ocr_status === 'skipped' && (
              <Alert color="gray" variant="light" mb="xs">
                OCR did not run. Install Tesseract and the languages in the admin settings, then run OCR again.
              </Alert>
            )}
            {doc.ocr_status === 'pending' && <Text size="sm" c="dimmed">OCR is running. This page updates when it finishes.</Text>}
            {doc.ocr_status === 'done' && (
              <ScrollArea.Autosize mah={360} className="ax-ocr-text">
                <Text component="pre" size="sm" m={0}>
                  {doc.text || 'No text was found on the pages.'}
                </Text>
              </ScrollArea.Autosize>
            )}
          </Box>
        </Stack>
      </div>

      <ShareDialog opened={sharing} onClose={() => setSharing(false)} docId={doc.id} title={doc.title} />

      <DeleteDocumentDialog
        opened={confirmDelete}
        docId={doc.id}
        inTrash={inTrash}
        trashId={trash?.id}
        onClose={() => setConfirmDelete(false)}
        onDone={() => {
          setConfirmDelete(false)
          navigate(inTrash || trash === undefined ? libraryLocation : `/library?folder=${trash.id}`)
        }}
      />
    </Box>
  )
}
