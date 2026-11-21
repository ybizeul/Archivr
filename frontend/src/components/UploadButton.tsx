import { ActionIcon, Button, FileButton } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconUpload } from '@tabler/icons-react'
import { useNavigate } from 'react-router-dom'
import { useUploadDocument } from '../api/queries'
import { errorMessage } from '../lib/format'

interface UploadButtonProps {
  variant?: 'filled' | 'default'
  size?: 'xs' | 'sm' | 'md'
  /** Icon only, for narrow headers. The label is kept for screen readers. */
  compact?: boolean
}

/** Sends a scan or PDF to the INBOX. Duplicates and unsupported files are reported, not silently dropped. */
export function UploadButton({ variant = 'default', size = 'sm', compact = false }: UploadButtonProps) {
  const upload = useUploadDocument()
  const navigate = useNavigate()

  const onPick = (file: File | null) => {
    if (!file) return
    upload.mutate(file, {
      onSuccess: (doc) => {
        notifications.show({
          color: 'prussian',
          title: 'Added to the inbox',
          message: `No. ${doc.id} · ${doc.title}`,
        })
        void navigate(`/?doc=${doc.id}`)
      },
      onError: (err) => {
        const code = (err as Error & { code?: string }).code
        notifications.show({
          color: 'red',
          title: code === 'DUPLICATE' ? 'Already in the archive' : "Couldn't add this file",
          message: errorMessage(err),
        })
      },
    })
  }

  const accept = 'application/pdf,image/png,image/jpeg,image/tiff,image/webp'

  if (compact) {
    return (
      <FileButton onChange={onPick} accept={accept}>
        {(props) => (
          <ActionIcon {...props} variant="default" size="lg" aria-label="Upload a file" loading={upload.isPending}>
            <IconUpload size={18} stroke={1.6} />
          </ActionIcon>
        )}
      </FileButton>
    )
  }

  return (
    <FileButton onChange={onPick} accept={accept}>
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
}
