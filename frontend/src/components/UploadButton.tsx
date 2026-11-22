import { useState } from 'react'
import { ActionIcon, Button, FileButton, Group, Modal, Stack, Text } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconUpload } from '@tabler/icons-react'
import { useNavigate } from 'react-router-dom'
import { useUploadDocument } from '../api/queries'
import { errorMessage, formatBytes } from '../lib/format'
import { DateField } from './DateField'

interface UploadButtonProps {
  variant?: 'filled' | 'default'
  size?: 'xs' | 'sm' | 'md'
  /** Icon only, for narrow headers. The label is kept for screen readers. */
  compact?: boolean
}

/**
 * Sends a scan or PDF to the INBOX. After a file is chosen, a small dialog offers an optional "valid until"
 * date: once it has passed the document moves to Trash. Duplicates and unsupported files are reported, not silently dropped.
 */
export function UploadButton({ variant = 'default', size = 'sm', compact = false }: UploadButtonProps) {
  const upload = useUploadDocument()
  const navigate = useNavigate()
  const [file, setFile] = useState<File | null>(null)
  const [validUntil, setValidUntil] = useState('')

  const close = () => {
    if (upload.isPending) return
    setFile(null)
    setValidUntil('')
  }

  const send = () => {
    if (!file) return
    upload.mutate(
      { file, validUntil: validUntil || undefined },
      {
        onSuccess: (doc) => {
          setFile(null)
          setValidUntil('')
          notifications.show({
            color: 'prussian',
            title: 'Added to the inbox',
            message: `No. ${doc.id} · ${doc.title}`,
          })
          void navigate(`/inbox?doc=${doc.id}`)
        },
        onError: (err) => {
          const code = (err as Error & { code?: string }).code
          setFile(null)
          setValidUntil('')
          notifications.show({
            color: 'red',
            title: code === 'DUPLICATE' ? 'Already in the archive' : "Couldn't add this file",
            message: errorMessage(err),
          })
        },
      },
    )
  }

  const accept = 'application/pdf,image/png,image/jpeg,image/tiff,image/webp'

  const picker = compact ? (
    <FileButton onChange={setFile} accept={accept}>
      {(props) => (
        <ActionIcon {...props} variant="default" size="lg" aria-label="Upload a file" loading={upload.isPending}>
          <IconUpload size={18} stroke={1.6} />
        </ActionIcon>
      )}
    </FileButton>
  ) : (
    <FileButton onChange={setFile} accept={accept}>
      {(props) => (
        <Button
          {...props}
          variant={variant}
          size={size}
          loading={upload.isPending}
          leftSection={<IconUpload size={16} stroke={1.6} />}
        >
          Upload
        </Button>
      )}
    </FileButton>
  )

  return (
    <>
      {picker}
      <Modal opened={file !== null} onClose={close} title="Add to the archive" centered>
        <Stack>
          <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
            {file?.name}
            {file ? ` · ${formatBytes(file.size)}` : ''}
          </Text>
          <DateField
            label="Valid until"
            description="Optional. The document moves to Trash the day after this date."
            value={validUntil}
            onValueChange={setValidUntil}
            data-autofocus
          />
          <Group justify="flex-end">
            <Button variant="default" onClick={close} disabled={upload.isPending}>
              Cancel
            </Button>
            <Button onClick={send} loading={upload.isPending}>
              Upload
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  )
}
