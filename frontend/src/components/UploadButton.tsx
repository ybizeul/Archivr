import { ActionIcon, Button, FileButton } from '@mantine/core'
import { IconUpload } from '@tabler/icons-react'
import { useFileUpload } from '../lib/useFileUpload'

interface UploadButtonProps {
  variant?: 'filled' | 'default'
  size?: 'xs' | 'sm' | 'md'
  /** Icon only, for narrow headers. The label is kept for screen readers. */
  compact?: boolean
}

const accept = 'application/pdf,image/png,image/jpeg,image/tiff,image/webp'

/**
 * Sends scans or PDFs to the INBOX as soon as they are chosen. A valid-until date is set later from the inbox panel.
 * Files can also be dropped anywhere in the app (see GlobalDropZone).
 */
export function UploadButton({ variant = 'default', size = 'sm', compact = false }: UploadButtonProps) {
  const { uploadFiles, isPending } = useFileUpload()
  const onChange = (files: File[]) => {
    if (files.length > 0) void uploadFiles(files)
  }

  return (
    <FileButton onChange={onChange} accept={accept} multiple>
      {(props) =>
        compact ? (
          <ActionIcon {...props} variant="default" size="lg" aria-label="Upload a file" loading={isPending}>
            <IconUpload size={18} stroke={1.6} />
          </ActionIcon>
        ) : (
          <Button {...props} variant={variant} size={size} loading={isPending} leftSection={<IconUpload size={16} stroke={1.6} />}>
            Upload
          </Button>
        )
      }
    </FileButton>
  )
}
