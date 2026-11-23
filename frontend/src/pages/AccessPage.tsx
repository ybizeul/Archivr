import { useState } from 'react'
import {
  ActionIcon,
  Alert,
  Button,
  Code,
  CopyButton,
  Group,
  Loader,
  Modal,
  Paper,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconCheck, IconCopy, IconTrash } from '@tabler/icons-react'
import { useTokenMutations, useTokens } from '../api/queries'
import type { ApiToken, CreatedApiToken } from '../api/types'
import { errorMessage, formatDateTime } from '../lib/format'

const EXPIRY = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
  { value: 'never', label: 'Never' },
]

const mcpUrl = () => `${window.location.origin}/api/v1/mcp`

const clientConfig = (token: string) =>
  JSON.stringify(
    { mcpServers: { archivr: { url: mcpUrl(), headers: { Authorization: `Bearer ${token}` } } } },
    null,
    2,
  )

function CopyField({ label, value }: { label: string; value: string }) {
  return (
    <TextInput
      label={label}
      value={value}
      readOnly
      onFocus={(e) => e.currentTarget.select()}
      rightSectionWidth={96}
      rightSection={
        <CopyButton value={value}>
          {({ copied, copy }) => (
            <Button
              size="compact-sm"
              variant="light"
              onClick={copy}
              leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
            >
              {copied ? 'Copied' : 'Copy'}
            </Button>
          )}
        </CopyButton>
      }
    />
  )
}

/** Personal access tokens, and how to point an MCP client at the archive. */
export function AccessPage() {
  const tokens = useTokens()
  const { create, revoke } = useTokenMutations()
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState<string>('90')
  const [created, setCreated] = useState<CreatedApiToken | null>(null)
  const [toRevoke, setToRevoke] = useState<ApiToken | null>(null)

  const add = () => {
    create.mutate(
      { name: name.trim(), expires_in_days: expiry === 'never' ? undefined : Number(expiry) },
      {
        onSuccess: (tok) => {
          setCreated(tok)
          setName('')
        },
        onError: (err) => notifications.show({ color: 'red', title: "Couldn't create the token", message: errorMessage(err) }),
      },
    )
  }

  return (
    <Stack gap="xl" p={{ base: 'sm', sm: 'xl' }} maw={900}>
      <div>
        <Title order={2}>API access</Title>
        <Text c="dimmed" size="sm">
          Let an MCP client such as Cursor or Claude browse and search the archive in your name. It can read what
          you can read, private folders included, and it cannot change anything.
        </Text>
      </div>

      <Paper withBorder p="md">
        <Title order={4} mb="sm">
          Server address
        </Title>
        <CopyField label="MCP endpoint" value={mcpUrl()} />
        <Text size="sm" c="dimmed" mt="xs">
          Send a token below as <Code>Authorization: Bearer …</Code>.
        </Text>
      </Paper>

      <Paper withBorder p="md">
        <Title order={4} mb="sm">
          New token
        </Title>
        <Group align="flex-end" gap="sm" wrap="wrap">
          <TextInput
            label="Name"
            description="What it is for, for example “Cursor on my laptop”."
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            maxLength={100}
            w={300}
          />
          <Select label="Expires after" data={EXPIRY} value={expiry} onChange={(v) => setExpiry(v ?? '90')} allowDeselect={false} w={150} />
          <Button onClick={add} loading={create.isPending} disabled={!name.trim()}>
            Create token
          </Button>
        </Group>
      </Paper>

      {tokens.isPending ? (
        <Loader size="sm" />
      ) : (tokens.data ?? []).length === 0 ? (
        <Text size="sm" c="dimmed">
          You have no tokens.
        </Text>
      ) : (
        <Table verticalSpacing="sm" className="ax-stack-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Token</Table.Th>
              <Table.Th>Last used</Table.Th>
              <Table.Th>Expires</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(tokens.data ?? []).map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td data-label="Name">
                  <Text size="sm" fw={600}>
                    {t.name}
                  </Text>
                </Table.Td>
                <Table.Td className="ax-mono" data-label="Token">
                  {t.prefix}…
                </Table.Td>
                <Table.Td data-label="Last used">{t.last_used_at ? formatDateTime(t.last_used_at) : 'Never'}</Table.Td>
                <Table.Td data-label="Expires">{t.expires_at ? formatDateTime(t.expires_at) : 'Never'}</Table.Td>
                <Table.Td className="ax-actions-cell">
                  <Group justify="flex-end">
                    <Tooltip label="Revoke token">
                      <ActionIcon variant="subtle" color="oxide" aria-label={`Revoke ${t.name}`} onClick={() => setToRevoke(t)}>
                        <IconTrash size={16} stroke={1.6} />
                      </ActionIcon>
                    </Tooltip>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}

      <Modal opened={created !== null} onClose={() => setCreated(null)} title="Token created" size="lg" centered>
        {created && (
          <Stack>
            <Alert color="carmine" variant="light" title="Copy it now">
              This is the only time the token is shown. If you lose it, revoke it and make a new one.
            </Alert>
            <CopyField label="Token" value={created.token} />
            <div>
              <Group justify="space-between" mb={4}>
                <Text size="sm" fw={500}>
                  Client configuration
                </Text>
                <CopyButton value={clientConfig(created.token)}>
                  {({ copied, copy }) => (
                    <Button size="compact-xs" variant="subtle" onClick={copy}>
                      {copied ? 'Copied' : 'Copy'}
                    </Button>
                  )}
                </CopyButton>
              </Group>
              <Code block style={{ overflowX: 'auto' }}>
                {clientConfig(created.token)}
              </Code>
              <Text size="xs" c="dimmed" mt={4}>
                For Cursor, put this in <Code>.cursor/mcp.json</Code> or <Code>~/.cursor/mcp.json</Code>.
              </Text>
            </div>
            <Group justify="flex-end">
              <Button onClick={() => setCreated(null)}>Done</Button>
            </Group>
          </Stack>
        )}
      </Modal>

      <Modal opened={toRevoke !== null} onClose={() => setToRevoke(null)} title={`Revoke “${toRevoke?.name ?? ''}”?`} centered>
        <Stack>
          <Text size="sm">Any tool using this token stops working at once. This cannot be undone.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setToRevoke(null)}>
              Cancel
            </Button>
            <Button
              color="oxide"
              loading={revoke.isPending}
              onClick={() =>
                toRevoke &&
                revoke.mutate(toRevoke.id, {
                  onSuccess: () => setToRevoke(null),
                  onError: (err) => notifications.show({ color: 'red', title: "Couldn't revoke the token", message: errorMessage(err) }),
                })
              }
            >
              Revoke token
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  )
}
