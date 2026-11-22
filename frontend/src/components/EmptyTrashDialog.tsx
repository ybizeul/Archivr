import { Button, Group, Modal, Stack, Text } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useEmptyTrash } from '../api/queries'
import { errorMessage } from '../lib/format'

/**
 * Permanently deletes every document in Trash. The count is how many the folder
 * held when the dialog opened; the server deletes whatever is still there.
 */
export function EmptyTrashDialog({
  opened,
  count,
  onClose,
}: {
  opened: boolean
  count: number
  onClose: () => void
}) {
  const empty = useEmptyTrash()

  const confirm = () => {
    empty.mutate(undefined, {
      onSuccess: (res) => {
        const n = res.deleted
        const message = n === 0 ? 'Trash is already empty' : n === 1 ? 'Deleted 1 document' : `Deleted ${n} documents`
        notifications.show({ color: 'prussian', message })
        onClose()
      },
      onError: (err) => notifications.show({ color: 'red', title: "Couldn't empty Trash", message: errorMessage(err) }),
    })
  }

  const what =
    count === 1
      ? 'This removes the document in Trash and its file. The number is not reused.'
      : `This removes all ${count} documents in Trash and their files. The numbers are not reused.`

  return (
    <Modal
      opened={opened}
      onClose={() => {
        if (!empty.isPending) onClose()
      }}
      title="Empty Trash?"
      centered
    >
      <Stack>
        <Text size="sm">{what} This cannot be undone.</Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose} disabled={empty.isPending}>
            {count === 1 ? 'Keep it' : 'Keep them'}
          </Button>
          <Button color="oxide" loading={empty.isPending} onClick={confirm}>
            Empty Trash
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
