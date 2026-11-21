import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { Alert, Anchor, Autocomplete, Badge, Box, Button, CopyButton, Group, Loader, Modal, ScrollArea, Select, Stack, TagsInput, Text, TextInput, Title, ActionIcon, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconArrowLeft, IconCopy, IconCheck } from '@tabler/icons-react'
import { useCorrespondents, useDeleteDocument, useDocument, useFolders, usePatchDocument, useReOCR, useTags, useTypes } from '../api/queries'
import type { DocumentDetail } from '../api/types'
import { DateField } from '../components/DateField'
import { DocViewer } from '../components/DocViewer'
import { SheetBar } from '../components/SheetBar'
import { errorMessage, folderLabel, formatBytes, formatDate, formatDateTime, OCR_LABEL } from '../lib/format'

export function DocumentPage() {
  const { id: rawId } = useParams()
  const id = Number(rawId)
  const doc = useDocument(Number.isFinite(id) && id > 0 ? id : undefined)

  if (doc.isPending) {
    return (
      <Box p="xl">
        <Loader size="sm" />
      </Box>
    )
  }
  if (doc.isError || !doc.data) {
    return (
      <Stack p="xl" maw={480}>
        <Title order={3}>No such document</Title>
        <Text c="dimmed">It may have been deleted. Check the number and try again.</Text>
        <Anchor component={Link} to="/library">
          Back to the library
        </Anchor>
      </Stack>
    )
  }
  return <DocumentView key={doc.data.id} doc={doc.data} />
}

function DocumentView({ doc }: { doc: DocumentDetail }) {
  const navigate = useNavigate()
  const folders = useFolders()
  const types = useTypes()
  const correspondents = useCorrespondents()
  const tags = useTags()
  const save = usePatchDocument()
  const reocr = useReOCR()
  const remove = useDeleteDocument()

  const [title, setTitle] = useState(doc.title)
  const [date, setDate] = useState(doc.document_date ?? '')
  const [typeId, setTypeId] = useState<string | null>(doc.type_id ? String(doc.type_id) : null)
  const [correspondent, setCorrespondent] = useState(doc.correspondent)
  const [tagList, setTagList] = useState<string[]>(doc.tags)
  const [folderId, setFolderId] = useState<string>(String(doc.folder_id))
  const [confirmDelete, setConfirmDelete] = useState(false)
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
          type_id: typeId ? Number(typeId) : 0,
          correspondent: correspondent.trim(),
          tags: tagList,
          folder_id: Number(folderId),
        },
      },
      {
        onSuccess: () => notifications.show({ color: 'prussian', message: `Saved No. ${doc.id}` }),
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
      },
    )
  }

  const folderData = (folders.data ?? []).map((f) => ({
    value: String(f.id),
    label: f.is_system ? 'Inbox' : folderLabel(f.path),
  }))

  return (
    <Box className="ax-detail">
      <Group justify="space-between" mb="md" wrap="nowrap">
        <Anchor component={Link} to="/library" size="sm" className="ax-back">
          <Group gap={6} wrap="nowrap">
            <IconArrowLeft size={16} stroke={1.6} />
            Library
          </Group>
        </Anchor>
        <Button variant="subtle" color="oxide" size="xs" onClick={() => setConfirmDelete(true)}>
          Delete
        </Button>
      </Group>

      <div className="ax-detail-grid">
        <Box className="ax-sheet ax-detail-sheet">
          <SheetBar id={doc.id} />
          <DocViewer key={doc.id} id={doc.id} mime={doc.mime} title={doc.title} />
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
              <Select
                label="Type"
                data={(types.data ?? []).map((t) => ({ value: String(t.id), label: t.name }))}
                value={typeId}
                onChange={setTypeId}
                clearable
                searchable
                placeholder="No type"
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

      <Modal opened={confirmDelete} onClose={() => setConfirmDelete(false)} title={`Delete No. ${doc.id}?`} centered>
        <Stack>
          <Text size="sm">
            This removes the file and its search entry. The number is not reused. This cannot be undone.
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setConfirmDelete(false)}>
              Keep it
            </Button>
            <Button
              color="oxide"
              loading={remove.isPending}
              onClick={() =>
                remove.mutate(doc.id, {
                  onSuccess: () => {
                    notifications.show({ color: 'prussian', message: `Deleted No. ${doc.id}` })
                    navigate('/library')
                  },
                  onError: (err) => notifications.show({ color: 'red', title: "Couldn't delete", message: errorMessage(err) }),
                })
              }
            >
              Delete document
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Box>
  )
}
