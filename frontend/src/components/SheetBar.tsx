import { Anchor, Group } from '@mantine/core'
import { IconExternalLink } from '@tabler/icons-react'

/** The label strip above a light table: the document number, and a way to open the raw file. */
export function SheetBar({ id }: { id: number }) {
  return (
    <Group className="ax-sheet-bar" justify="space-between" wrap="nowrap" gap="sm">
      <span className="ax-mono ax-accent">No. {id}</span>
      <Anchor href={`/api/v1/documents/${id}/file`} target="_blank" rel="noopener" size="xs" className="ax-open-file">
        <Group gap={4} wrap="nowrap">
          Open file
          <IconExternalLink size={12} stroke={1.6} />
        </Group>
      </Anchor>
    </Group>
  )
}
