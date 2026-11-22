import { useState } from 'react'
import { Button, FileButton, Group, Modal, Stack, Text } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconFileUpload } from '@tabler/icons-react'
import { useReplaceFile } from '../api/queries'
import { errorMessage, formatBytes } from '../lib/format'

const ACCEPT = 'application/pdf,image/png,image/jpeg,image/tiff,image/webp'

/**
 * Swaps the file behind a document for a new upload. The number, title, folder and tags stay.
 * The old file is removed, so the user confirms the chosen file first.
 */
export function ReplaceFileButton({ docId }: { docId: number }) {
  const replace = useReplaceFile()
  const [file, setFile] = useState<File | null>(null)

  const close = () => {
    if (!replace.isPending) setFile(null)
  }

  const send = () => {
    if (!file) return
    replace.mutate(
      { id: docId, file },
      {
        onSuccess: () => {
          setFile(null)
          notifications.show({ color: 'prussian', message: `Replaced the file of No. ${docId}` })
        },
        onError: (err) => {
          setFile(null)
          notifications.show({ color: 'red', title: "Couldn't replace the file", message: errorMessage(err) })
        },
      },
    )
  }

  return (
    <>
      <FileButton onChange={setFile} accept={ACCEPT}>
        {(props) => (
          <Button {...props} variant="default" size="xs" leftSection={<IconFileUpload size={14} stroke={1.6} />}>
            Replace file
          </Button>
        )}
      </FileButton>
      <Modal opened={file !== null} onClose={close} title={`Replace the file of No. ${docId}?`} centered>
        <Stack>
          <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
            {file?.name}
            {file ? ` · ${formatBytes(file.size)}` : ''}
          </Text>
          <Text size="sm">
            The current file is removed and cannot be recovered. The number, title, folder and tags stay, the
            rotation is reset, and the text is read again.
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={close} disabled={replace.isPending}>
              Keep the current file
            </Button>
            <Button color="oxide" onClick={send} loading={replace.isPending}>
              Replace
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  )
}
