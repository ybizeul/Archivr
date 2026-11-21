import { useState } from 'react'
import { ActionIcon, Badge, Button, Group, Loader, Modal, Paper, PasswordInput, Select, Stack, Table, Text, TextInput, Title, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconKey, IconTrash } from '@tabler/icons-react'
import { useUserMutations, useUsers } from '../../api/queries'
import type { UserRow } from '../../api/types'
import { useAuth } from '../../auth/context'
import { errorMessage } from '../../lib/format'

const ROLES = [
  { value: 'admin', label: 'Admin' },
  { value: 'user', label: 'User' },
]

export function UsersSettings() {
  const users = useUsers()
  const { user: me } = useAuth()
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [role, setRole] = useState<string>('user')
  const [password, setPassword] = useState('')
  const { create } = useUserMutations()

  const add = () => {
    create.mutate(
      { email: email.trim(), name: name.trim(), role, password: password || undefined },
      {
        onSuccess: () => {
          setEmail('')
          setName('')
          setRole('user')
          setPassword('')
          notifications.show({ color: 'prussian', message: 'Account added' })
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't add the account", message: errorMessage(err) }),
      },
    )
  }

  return (
    <Stack gap="xl" maw={860}>
      <Paper withBorder p="md" className="ax-user-form">
        <Title order={4} mb="sm">
          Add an account
        </Title>
        <Group align="flex-end" gap="sm" wrap="wrap">
          <TextInput label="Email" value={email} onChange={(e) => setEmail(e.currentTarget.value)} autoComplete="off" w={230} />
          <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} w={180} />
          <Select label="Role" data={ROLES} value={role} onChange={(v) => setRole(v ?? 'user')} allowDeselect={false} w={130} />
          <PasswordInput
            label="Password"
            description="Optional. Leave blank for SSO-only accounts. At least 12 characters."
            value={password}
            onChange={(e) => setPassword(e.currentTarget.value)}
            autoComplete="new-password"
            w={260}
          />
          <Button onClick={add} loading={create.isPending} disabled={!email.trim()}>
            Add account
          </Button>
        </Group>
      </Paper>

      {users.isPending ? (
        <Loader size="sm" />
      ) : (
        <Table verticalSpacing="sm" className="ax-stack-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Email</Table.Th>
              <Table.Th>Role</Table.Th>
              <Table.Th>Sign-in</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(users.data ?? []).map((u) => (
              <UserRowView key={u.id} row={u} isSelf={u.id === me?.id} />
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  )
}

function UserRowView({ row, isSelf }: { row: UserRow; isSelf: boolean }) {
  const { update, remove } = useUserMutations()
  const [resetOpen, setResetOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const fail = (title: string) => (err: unknown) => notifications.show({ color: 'red', title, message: errorMessage(err) })

  return (
    <Table.Tr>
      <Table.Td data-label="Name">
        <Text size="sm" fw={600}>
          {row.name || '—'}
          {isSelf && (
            <Text component="span" size="xs" c="dimmed" ml={6}>
              (you)
            </Text>
          )}
        </Text>
      </Table.Td>
      <Table.Td className="ax-mono ax-wrap-cell" data-label="Email">{row.email}</Table.Td>
      <Table.Td data-label="Role">
        <Select
          aria-label={`Role of ${row.email}`}
          data={ROLES}
          value={row.role}
          allowDeselect={false}
          size="xs"
          w={110}
          disabled={isSelf}
          onChange={(v) => v && v !== row.role && update.mutate({ id: row.id, role: v }, { onError: fail("Couldn't change the role") })}
        />
      </Table.Td>
      <Table.Td data-label="Sign-in">
        <Badge variant="outline" color={row.has_password ? 'gray' : 'prussian'} radius="sm">
          {row.has_password ? 'Password' : 'SSO only'}
        </Badge>
      </Table.Td>
      <Table.Td className="ax-actions-cell">
        <Group gap={4} wrap="nowrap" justify="flex-end">
          <Tooltip label="Set a new password">
            <ActionIcon variant="subtle" color="gray" aria-label={`Set password for ${row.email}`} onClick={() => setResetOpen(true)}>
              <IconKey size={16} stroke={1.6} />
            </ActionIcon>
          </Tooltip>
          <Tooltip label={isSelf ? 'You cannot delete your own account' : 'Delete account'}>
            <ActionIcon
              variant="subtle"
              color="oxide"
              aria-label={`Delete ${row.email}`}
              disabled={isSelf}
              onClick={() => setDeleteOpen(true)}
            >
              <IconTrash size={16} stroke={1.6} />
            </ActionIcon>
          </Tooltip>
        </Group>
      </Table.Td>

      {resetOpen && <ResetPassword user={row} onClose={() => setResetOpen(false)} />}
      <Modal opened={deleteOpen} onClose={() => setDeleteOpen(false)} title={`Delete ${row.email}?`} centered>
        <Stack>
          <Text size="sm">Their documents stay in the archive. Only their sign-in is removed.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              color="oxide"
              loading={remove.isPending}
              onClick={() =>
                remove.mutate(row.id, {
                  onSuccess: () => setDeleteOpen(false),
                  onError: fail("Couldn't delete the account"),
                })
              }
            >
              Delete account
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Table.Tr>
  )
}

function ResetPassword({ user, onClose }: { user: UserRow; onClose: () => void }) {
  const { setPassword } = useUserMutations()
  const [pw, setPw] = useState('')
  const [error, setError] = useState<string | undefined>()
  const save = () => {
    if (pw.length < 12) {
      setError('At least 12 characters')
      return
    }
    setPassword.mutate(
      { id: user.id, password: pw },
      {
        onSuccess: () => {
          notifications.show({ color: 'prussian', message: `Password set for ${user.email}` })
          onClose()
        },
        onError: (err) => setError(errorMessage(err)),
      },
    )
  }
  return (
    <Modal opened onClose={onClose} title={`Set password for ${user.email}`} centered>
      <Stack>
        <PasswordInput
          label="New password"
          description="At least 12 characters."
          value={pw}
          onChange={(e) => setPw(e.currentTarget.value)}
          error={error}
          autoComplete="new-password"
          autoFocus
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={save} loading={setPassword.isPending}>
            Set password
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
