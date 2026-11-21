import { useState } from 'react'
import { ActionIcon, Button, ColorInput, Group, Loader, Stack, Table, Text, TextInput, Title, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTagMutations, useTags, useTypeMutations, useTypes } from '../../api/queries'
import type { DocType, Tag } from '../../api/types'
import { errorMessage } from '../../lib/format'

const DEFAULT_COLOR = '#1F4E79'

/** Document types (admin only) and tags (anyone can rename or remove, the server applies the same rules). */
export function CatalogSettings() {
  return (
    <Stack gap="xl" maw={720}>
      <TypesSection />
      <TagsSection />
    </Stack>
  )
}

function TypesSection() {
  const types = useTypes()
  const { create } = useTypeMutations()
  const [name, setName] = useState('')
  const [color, setColor] = useState(DEFAULT_COLOR)

  const add = () => {
    const trimmed = name.trim()
    if (!trimmed) return
    create.mutate(
      { name: trimmed, color },
      {
        onSuccess: () => {
          setName('')
          setColor(DEFAULT_COLOR)
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't add the type", message: errorMessage(err) }),
      },
    )
  }

  return (
    <Stack gap="sm">
      <Title order={4}>Document types</Title>
      {types.isPending ? (
        <Loader size="sm" />
      ) : (
        <Table verticalSpacing="xs" className="ax-stack-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Colour</Table.Th>
              <Table.Th>Documents</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(types.data ?? []).map((t) => (
              <TypeRow key={`${t.id}-${t.name}-${t.color}`} type={t} />
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Group align="flex-end" gap="sm" wrap="wrap">
        <TextInput label="New type" value={name} onChange={(e) => setName(e.currentTarget.value)} placeholder="Invoice" w={220} />
        <ColorInput label="Colour" value={color} onChange={setColor} w={140} format="hex" swatches={['#1F4E79', '#6FA8D6', '#B0306A', '#A8402A', '#5B6B7A']} />
        <Button leftSection={<IconPlus size={16} />} onClick={add} loading={create.isPending} disabled={!name.trim()}>
          Add type
        </Button>
      </Group>
    </Stack>
  )
}

function TypeRow({ type }: { type: DocType }) {
  const { update, remove } = useTypeMutations()
  const [name, setName] = useState(type.name)
  const [color, setColor] = useState(type.color)
  const dirty = name.trim() !== type.name || color !== type.color

  const save = () =>
    update.mutate(
      { id: type.id, name: name.trim(), color },
      { onError: (err) => notifications.show({ color: 'red', title: "Couldn't save the type", message: errorMessage(err) }) },
    )

  return (
    <Table.Tr>
      <Table.Td data-label="Name">
        <TextInput value={name} onChange={(e) => setName(e.currentTarget.value)} aria-label={`Name of ${type.name}`} size="sm" />
      </Table.Td>
      <Table.Td data-label="Colour">
        <ColorInput value={color} onChange={setColor} format="hex" size="sm" w={130} aria-label={`Colour of ${type.name}`} />
      </Table.Td>
      <Table.Td className="ax-mono" data-label="Documents">{type.document_count}</Table.Td>
      <Table.Td className="ax-actions-cell">
        <Group gap={4} wrap="nowrap" justify="flex-end">
          {dirty && (
            <Button size="xs" onClick={save} loading={update.isPending} disabled={!name.trim()}>
              Save
            </Button>
          )}
          <Tooltip label={type.document_count > 0 ? 'In use by documents' : 'Delete type'}>
            <ActionIcon
              variant="subtle"
              color="oxide"
              aria-label={`Delete ${type.name}`}
              disabled={type.document_count > 0}
              onClick={() =>
                remove.mutate(type.id, {
                  onError: (err) => notifications.show({ color: 'red', title: "Couldn't delete", message: errorMessage(err) }),
                })
              }
            >
              <IconTrash size={16} stroke={1.6} />
            </ActionIcon>
          </Tooltip>
        </Group>
      </Table.Td>
    </Table.Tr>
  )
}

function TagsSection() {
  const tags = useTags()
  return (
    <Stack gap="sm">
      <Title order={4}>Tags</Title>
      {tags.isPending ? (
        <Loader size="sm" />
      ) : (tags.data ?? []).length === 0 ? (
        <Text size="sm" c="dimmed">
          Tags appear here once a document has one.
        </Text>
      ) : (
        <Table verticalSpacing="xs" className="ax-stack-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Tag</Table.Th>
              <Table.Th>Documents</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(tags.data ?? []).map((t) => (
              <TagRow key={`${t.id}-${t.name}`} tag={t} />
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  )
}

function TagRow({ tag }: { tag: Tag }) {
  const { rename, remove } = useTagMutations()
  const [name, setName] = useState(tag.name)
  const dirty = name.trim() !== tag.name && name.trim() !== ''
  return (
    <Table.Tr>
      <Table.Td data-label="Tag">
        <TextInput
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          onKeyDown={(e) => e.key === 'Enter' && dirty && rename.mutate({ id: tag.id, name: name.trim() })}
          aria-label={`Name of tag ${tag.name}`}
          size="sm"
        />
      </Table.Td>
      <Table.Td className="ax-mono" data-label="Documents">{tag.document_count}</Table.Td>
      <Table.Td className="ax-actions-cell">
        <Group gap={4} wrap="nowrap" justify="flex-end">
          {dirty && (
            <Button size="xs" onClick={() => rename.mutate({ id: tag.id, name: name.trim() })} loading={rename.isPending}>
              Rename
            </Button>
          )}
          <ActionIcon
            variant="subtle"
            color="oxide"
            aria-label={`Remove tag ${tag.name} from every document`}
            onClick={() => remove.mutate(tag.id, { onError: (err) => notifications.show({ color: 'red', message: errorMessage(err) }) })}
          >
            <IconTrash size={16} stroke={1.6} />
          </ActionIcon>
        </Group>
      </Table.Td>
    </Table.Tr>
  )
}
