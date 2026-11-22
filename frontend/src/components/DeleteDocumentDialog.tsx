import { Button, Group, Modal, Stack, Text } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useDeleteDocument, usePatchDocument } from '../api/queries'
import { errorMessage } from '../lib/format'

/**
 * Delete files the document into Trash. From Trash, the same dialog removes it for good.
 */
export function DeleteDocumentDialog({
  opened,
  docId,
  inTrash,
  trashId,
  onClose,
  onDone,
}: {
  opened: boolean
  docId: number
  inTrash: boolean
  trashId?: number
  onClose: () => void
  onDone: () => void
}) {
  const move = usePatchDocument()
  const remove = useDeleteDocument()
  const pending = move.isPending || remove.isPending

  const confirm = () => {
    if (inTrash) {
      remove.mutate(docId, {
        onSuccess: () => {
          notifications.show({ color: 'prussian', message: `Deleted No. ${docId}` })
          onDone()
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't delete", message: errorMessage(err) }),
      })
      return
    }
    if (trashId === undefined) {
      notifications.show({ color: 'red', title: "Couldn't move to Trash", message: 'Trash is not available yet.' })
      return
    }
    move.mutate(
      { id: docId, patch: { folder_id: trashId } },
      {
        onSuccess: () => {
          notifications.show({ color: 'prussian', message: `Moved No. ${docId} to Trash` })
          onDone()
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't move to Trash", message: errorMessage(err) }),
      },
    )
  }

  return (
    <Modal
      opened={opened}
      onClose={() => {
        if (!pending) onClose()
      }}
      title={inTrash ? `Delete No. ${docId} permanently?` : `Move No. ${docId} to Trash?`}
      centered
    >
      <Stack>
        <Text size="sm">
          {inTrash
            ? 'This removes the file and its search entry. The number is not reused. This cannot be undone.'
            : 'The document leaves its folder and waits in Trash. Open Trash to file it somewhere else, or delete it there for good.'}
        </Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose} disabled={pending}>
            Keep it
          </Button>
          <Button color="oxide" loading={pending} onClick={confirm}>
            {inTrash ? 'Delete permanently' : 'Move to Trash'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
