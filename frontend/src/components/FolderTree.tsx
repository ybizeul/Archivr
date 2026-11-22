import { useMemo, useState } from 'react'
import { ActionIcon, Box, Button, Group, Menu, Modal, Select, Stack, Text, TextInput, UnstyledButton } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconDots, IconFolder, IconFolderOpen, IconPlus, IconTrash } from '@tabler/icons-react'
import { useFolderMutations, useFolders } from '../api/queries'
import type { Folder } from '../api/types'
import { errorMessage, folderLabel, folderName, isTrash } from '../lib/format'

interface FolderTreeProps {
  selected?: number
  onSelect: (id: number | undefined) => void
}

interface Node {
  folder: Folder
  children: Node[]
}

function buildTree(folders: Folder[]): Node[] {
  const byParent = new Map<number | null, Folder[]>()
  for (const f of folders) {
    const key = f.parent_id ?? null
    byParent.set(key, [...(byParent.get(key) ?? []), f])
  }
  const make = (parent: number | null): Node[] =>
    (byParent.get(parent) ?? [])
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
      .map((folder) => ({ folder, children: make(folder.id) }))
  return make(null)
}

/** The folder tree in the library. INBOX and Trash are system folders: they are shown but cannot be changed. Trash sits at the bottom, apart from the folders you file into. */
export function FolderTree({ selected, onSelect }: FolderTreeProps) {
  const folders = useFolders()
  const all = folders.data ?? []
  const trash = all.find(isTrash)
  const tree = useMemo(() => buildTree((folders.data ?? []).filter((f) => !isTrash(f))), [folders.data])
  const [creating, setCreating] = useState<{ parent?: number } | null>(null)
  const [renaming, setRenaming] = useState<Folder | null>(null)
  const [moving, setMoving] = useState<Folder | null>(null)
  const [deleting, setDeleting] = useState<Folder | null>(null)

  const renderNode = (node: Node, depth: number) => {
    const f = node.folder
    const active = selected === f.id
    return (
      <Box key={f.id}>
        <Group gap={2} wrap="nowrap" className={`ax-tree-row${active ? ' active' : ''}`} style={{ paddingLeft: depth * 14 }}>
          <UnstyledButton
            className="ax-tree-link"
            onClick={() => onSelect(f.id)}
            aria-current={active ? 'true' : undefined}
          >
            {active ? <IconFolderOpen size={16} stroke={1.6} /> : <IconFolder size={16} stroke={1.6} />}
            <Text component="span" size="sm" truncate className="ax-tree-name">
              {folderName(f)}
            </Text>
            <Text component="span" size="xs" c="dimmed" className="ax-mono">
              {f.document_count}
            </Text>
          </UnstyledButton>
          {!f.is_system && (
            <Menu position="bottom-end" withinPortal>
              <Menu.Target>
                <ActionIcon variant="subtle" color="gray" size="sm" aria-label={`Actions for ${f.name}`}>
                  <IconDots size={14} stroke={1.6} />
                </ActionIcon>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => setCreating({ parent: f.id })}>New subfolder</Menu.Item>
                <Menu.Item onClick={() => setRenaming(f)}>Rename</Menu.Item>
                <Menu.Item onClick={() => setMoving(f)}>Move</Menu.Item>
                <Menu.Divider />
                <Menu.Item color="red" onClick={() => setDeleting(f)}>
                  Delete
                </Menu.Item>
              </Menu.Dropdown>
            </Menu>
          )}
        </Group>
        {node.children.map((c) => renderNode(c, depth + 1))}
      </Box>
    )
  }

  return (
    <Stack gap={6} className="ax-tree">
      <Group justify="space-between" wrap="nowrap">
        <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
          Folders
        </Text>
        <ActionIcon variant="subtle" color="prussian" aria-label="New folder" onClick={() => setCreating({})}>
          <IconPlus size={16} stroke={1.6} />
        </ActionIcon>
      </Group>

      <UnstyledButton
        className={`ax-tree-link ax-tree-all${selected === undefined ? ' active' : ''}`}
        onClick={() => onSelect(undefined)}
        aria-current={selected === undefined ? 'true' : undefined}
      >
        <Text component="span" size="sm" fw={500}>
          All documents
        </Text>
      </UnstyledButton>

      {tree.map((n) => renderNode(n, 0))}

      {trash && (
        <Box className="ax-tree-trash">
          <Group gap={2} wrap="nowrap" className={`ax-tree-row${selected === trash.id ? ' active' : ''}`}>
            <UnstyledButton
              className="ax-tree-link"
              onClick={() => onSelect(trash.id)}
              aria-current={selected === trash.id ? 'true' : undefined}
            >
              <IconTrash size={16} stroke={1.8} className="ax-trash-icon" />
              <Text component="span" size="sm" truncate className="ax-tree-name">
                {folderName(trash)}
              </Text>
              <Text component="span" size="xs" c={trash.document_count > 0 ? 'oxide' : 'dimmed'} className="ax-mono">
                {trash.document_count}
              </Text>
            </UnstyledButton>
          </Group>
        </Box>
      )}

      <FolderDialog
        opened={creating !== null}
        title="New folder"
        initialParent={creating?.parent}
        folders={folders.data ?? []}
        onClose={() => setCreating(null)}
        onSaved={(id) => {
          setCreating(null)
          onSelect(id)
        }}
      />
      {renaming && <FolderRename key={renaming.id} folder={renaming} onClose={() => setRenaming(null)} />}
      {moving && <FolderMove key={moving.id} folder={moving} folders={all} onClose={() => setMoving(null)} />}
      <FolderDelete folder={deleting} onClose={() => setDeleting(null)} onDeleted={() => onSelect(undefined)} />
    </Stack>
  )
}

function FolderDialog({
  opened,
  title,
  initialParent,
  folders,
  onClose,
  onSaved,
}: {
  opened: boolean
  title: string
  initialParent?: number
  folders: Folder[]
  onClose: () => void
  onSaved: (id: number) => void
}) {
  const { create } = useFolderMutations()
  const [name, setName] = useState('')
  const [parent, setParent] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const parentData = folders
    .filter((f) => !f.is_system)
    .map((f) => ({ value: String(f.id), label: f.path.split('/').join(' / ') }))

  // Until the user touches the picker, the folder they opened "New subfolder" from is the parent.
  const chosenParent = parent === null ? (initialParent ?? null) : parent === '' ? null : Number(parent)

  const submit = () => {
    const trimmed = name.trim()
    if (!trimmed) {
      setError('Enter a name')
      return
    }
    create.mutate(
      { name: trimmed, parent_id: chosenParent },
      {
        onSuccess: (f) => {
          setName('')
          setError(null)
          onSaved(f.id)
        },
        onError: (err) => setError(errorMessage(err)),
      },
    )
  }

  return (
    <Modal
      opened={opened}
      onClose={() => {
        setError(null)
        onClose()
      }}
      title={title}
      centered
    >
      <Stack>
        <TextInput
          label="Name"
          withAsterisk
          autoFocus
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          error={error}
          onKeyDown={(e) => e.key === 'Enter' && submit()}
        />
        <Select
          label="Inside"
          data={[{ value: '', label: 'Top level' }, ...parentData]}
          value={parent ?? (initialParent ? String(initialParent) : '')}
          comboboxProps={{ withinPortal: true }}
          onChange={(v) => setParent(v)}
          allowDeselect={false}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} loading={create.isPending}>
            Create folder
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function FolderRename({ folder, onClose }: { folder: Folder | null; onClose: () => void }) {
  const { update } = useFolderMutations()
  const [name, setName] = useState(folder?.name ?? '')
  const [error, setError] = useState<string | null>(null)
  if (!folder) return null
  const save = () => {
    update.mutate(
      { id: folder.id, name: name.trim() },
      {
        onSuccess: () => {
          setError(null)
          onClose()
        },
        onError: (err) => setError(errorMessage(err)),
      },
    )
  }
  return (
    <Modal opened onClose={onClose} title={`Rename ${folder.name}`} centered>
      <Stack>
        <TextInput
          label="Name"
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          error={error}
          autoFocus
          onKeyDown={(e) => e.key === 'Enter' && save()}
        />
        <Text size="xs" c="dimmed">
          Documents keep their numbers. Their files move with the folder.
        </Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={save} loading={update.isPending}>
            Rename
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function FolderMove({ folder, folders, onClose }: { folder: Folder; folders: Folder[]; onClose: () => void }) {
  const { update } = useFolderMutations()
  const [parent, setParent] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // A folder cannot sit inside itself or one of its subfolders. The server refuses that too.
  const underThis = (f: Folder) => f.path === folder.path || f.path.startsWith(`${folder.path}/`)
  const destinations = folders
    .filter((f) => !f.is_system && !underThis(f))
    .slice()
    .sort((a, b) => a.path.localeCompare(b.path, undefined, { sensitivity: 'base' }))
    .map((f) => ({ value: String(f.id), label: folderLabel(f.path) }))

  const current = folder.parent_id === null ? '' : String(folder.parent_id)
  const chosen = parent ?? current

  const save = () => {
    update.mutate(
      { id: folder.id, parent_id: chosen === '' ? 0 : Number(chosen) },
      {
        onSuccess: () => {
          notifications.show({ color: 'prussian', message: `Moved ${folder.name}` })
          onClose()
        },
        onError: (err) => setError(errorMessage(err)),
      },
    )
  }

  return (
    <Modal opened onClose={onClose} title={`Move ${folder.name}`} centered>
      <Stack>
        <Select
          label="Inside"
          data={[{ value: '', label: 'Top level' }, ...destinations]}
          value={chosen}
          onChange={(v) => {
            setParent(v ?? '')
            setError(null)
          }}
          allowDeselect={false}
          searchable
          comboboxProps={{ withinPortal: true }}
          error={error}
        />
        <Text size="xs" c="dimmed">
          The folder and everything in it move together. Documents keep their numbers.
        </Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={save} loading={update.isPending} disabled={chosen === current}>
            Move folder
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function FolderDelete({ folder, onClose, onDeleted }: { folder: Folder | null; onClose: () => void; onDeleted: () => void }) {
  const { remove } = useFolderMutations()
  if (!folder) return null
  const blocked = folder.document_count > 0 || folder.child_count > 0
  const confirm = () => {
    remove.mutate(folder.id, {
      onSuccess: () => {
        notifications.show({ color: 'prussian', message: `Deleted ${folder.name}` })
        onClose()
        onDeleted()
      },
      onError: (err) => {
        notifications.show({ color: 'red', title: "Couldn't delete the folder", message: errorMessage(err) })
        onClose()
      },
    })
  }
  return (
    <Modal opened onClose={onClose} title={`Delete ${folder.name}?`} centered>
      <Stack>
        {blocked ? (
          <Text size="sm">
            This folder still holds {folder.document_count} document{folder.document_count === 1 ? '' : 's'}
            {folder.child_count > 0 ? ' and subfolders' : ''}. Move them first, then delete the folder.
          </Text>
        ) : (
          <Text size="sm">The folder is empty, so nothing is lost.</Text>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          {!blocked && (
            <Button color="red" onClick={confirm} loading={remove.isPending}>
              Delete folder
            </Button>
          )}
        </Group>
      </Stack>
    </Modal>
  )
}