import { useState } from 'react'
import { ActionIcon, Button, Group, Popover, Select, Stack, Text, TextInput, Tooltip } from '@mantine/core'
import { IconFolderPlus } from '@tabler/icons-react'
import { useFolderMutations, useFolders } from '../api/queries'
import type { Folder } from '../api/types'
import { errorMessage, folderLabel } from '../lib/format'

// Stands for "no parent folder" in the picker, which cannot hold an empty value.
const TOP_LEVEL = 'top'

// The parent used last, shared by every New folder button, so making several subfolders
// under one folder does not mean choosing it each time. It is only read when a form opens.
let lastParent: string | null = null

/**
 * Makes one folder by hand: the user types its name and chooses where it goes, at the
 * top level or inside any folder, which is how a hierarchy is built one level at a time.
 * The new folder is handed back so the folder picker beside the button can select it.
 */
export function NewFolder({
  onCreated,
  defaultName = '',
  compact = false,
  disabled = false,
  selectedFolder = null,
}: {
  onCreated: (folder: Folder) => void
  /** Id of the folder currently selected in the picker beside the button, used as the default "Inside". */
  selectedFolder?: string | null
  defaultName?: string
  compact?: boolean
  disabled?: boolean
}) {
  const folders = useFolders().data ?? []
  const { create } = useFolderMutations()
  const [opened, setOpened] = useState(false)
  const [name, setName] = useState('')
  const [parent, setParent] = useState<string | null>(null)

  const parents = [
    { value: TOP_LEVEL, label: 'Top level' },
    ...folders.filter((f) => !f.is_system).map((f) => ({ value: String(f.id), label: folderLabel(f.path) })),
  ]
  const parentFolder = folders.find((f) => String(f.id) === parent)
  const trimmed = name.trim()
  const where = parentFolder ? folderLabel(parentFolder.path) : null

  const open = () => {
    setName(defaultName)
    // Prefer the folder currently selected beside the button, then the parent used last.
    // Either is skipped if it is gone or cannot hold folders (system folders).
    const usable = (id: string | null | undefined): id is string =>
      !!id && folders.some((f) => String(f.id) === id && !f.is_system)
    setParent(usable(selectedFolder) ? selectedFolder : usable(lastParent) ? lastParent : null)
    create.reset()
    setOpened(true)
  }

  const submit = () => {
    if (!trimmed || create.isPending) return
    create.mutate(
      { name: trimmed, parent_id: parent === null ? null : Number(parent) },
      {
        onSuccess: (folder) => {
          lastParent = parent
          setOpened(false)
          onCreated(folder)
        },
      },
    )
  }

  const toggle = () => (opened ? setOpened(false) : open())

  return (
    <Popover opened={opened} onChange={setOpened} position="bottom-end" width={340} withArrow trapFocus shadow="md">
      <Popover.Target>
        {compact ? (
          <Tooltip label="New folder">
            <ActionIcon variant="subtle" aria-label="New folder" disabled={disabled} onClick={toggle}>
              <IconFolderPlus size={16} />
            </ActionIcon>
          </Tooltip>
        ) : (
          <Button variant="default" size="xs" leftSection={<IconFolderPlus size={14} />} disabled={disabled} onClick={toggle}>
            New folder
          </Button>
        )}
      </Popover.Target>
      <Popover.Dropdown>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <Stack gap="xs">
            <TextInput
              label="Folder name"
              value={name}
              onChange={(e) => setName(e.currentTarget.value)}
              data-autofocus
              maxLength={100}
            />
            <Select
              label="Inside"
              data={parents}
              value={parent ?? TOP_LEVEL}
              onChange={(v) => setParent(v && v !== TOP_LEVEL ? v : null)}
              allowDeselect={false}
              searchable
              // The list must render inside this popover. In a portal it would count as a
              // click outside the popover, which closes it.
              comboboxProps={{ withinPortal: false }}
            />
            {trimmed && (
              <Text size="xs" c="dimmed">
                Creates {where ? `${where} / ` : ''}
                {trimmed}
              </Text>
            )}
            {create.isError && (
              <Text size="xs" c="red">
                {errorMessage(create.error)}
              </Text>
            )}
            <Group justify="flex-end" gap="xs">
              <Button size="xs" variant="default" onClick={() => setOpened(false)}>
                Cancel
              </Button>
              <Button size="xs" type="submit" loading={create.isPending} disabled={!trimmed}>
                Create folder
              </Button>
            </Group>
          </Stack>
        </form>
      </Popover.Dropdown>
    </Popover>
  )
}
