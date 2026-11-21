import { useState } from 'react'
import { Alert, Autocomplete, Button, Group, Loader, NumberInput, PasswordInput, Stack, Switch, Text, TextInput } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { keys, useIMAPSettings } from '../../api/queries'
import type { IMAPSettings } from '../../api/types'
import { errorMessage } from '../../lib/format'

const MASK = '********'

export function MailSettings() {
  const settings = useIMAPSettings()
  if (settings.isPending) return <Loader size="sm" />
  if (!settings.data) return <Text c="dimmed">The mail settings could not be loaded.</Text>
  return <MailForm key={JSON.stringify(settings.data)} initial={settings.data} />
}

function MailForm({ initial }: { initial: IMAPSettings }) {
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(initial.enabled)
  const [host, setHost] = useState(initial.imap_host)
  const [port, setPort] = useState<number | string>(initial.imap_port || 993)
  const [tls, setTLS] = useState(initial.imap_tls)
  const [user, setUser] = useState(initial.imap_user)
  const [password, setPassword] = useState(initial.imap_password_set ? MASK : '')
  const [mailbox, setMailbox] = useState(initial.imap_mailbox || 'INBOX')
  const [poll, setPoll] = useState<number | string>(initial.poll_interval_seconds || 300)
  const [mailboxes, setMailboxes] = useState<string[]>([])
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null)

  const body = () => ({
    enabled,
    imap_host: host.trim(),
    imap_port: Number(port) || 993,
    imap_tls: tls,
    imap_user: user.trim(),
    imap_password: password,
    imap_mailbox: mailbox.trim() || 'INBOX',
    poll_interval_seconds: Number(poll) || 300,
  })

  const save = useMutation({
    mutationFn: () => api.put<IMAPSettings>('/admin/settings/imap', body()),
    onSuccess: () => {
      notifications.show({ color: 'prussian', message: 'Mail settings saved' })
      void qc.invalidateQueries({ queryKey: keys.adminIMAP })
    },
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
  })

  // Tests and mailbox lists use the saved settings, so the form must be saved first.
  const test = useMutation({
    mutationFn: () => api.post<{ ok: boolean }>('/admin/settings/imap/test'),
    onSuccess: () => setTestResult({ ok: true, message: 'Connected and signed in.' }),
    onError: (err) => setTestResult({ ok: false, message: errorMessage(err) }),
  })

  const loadMailboxes = useMutation({
    mutationFn: () => api.get<{ items: string[] }>('/admin/settings/imap/mailboxes'),
    onSuccess: (res) => setMailboxes(res.items),
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't list mailboxes", message: errorMessage(err) }),
  })

  return (
    <Stack maw={620} gap="md">
      <Switch
        label="Import attachments from email"
        description="Unread messages in the mailbox are checked on each poll. Attachments are filed into INBOX, then the message is marked read."
        checked={enabled}
        onChange={(e) => setEnabled(e.currentTarget.checked)}
      />
      <Group grow align="flex-start">
        <TextInput label="Server" value={host} onChange={(e) => setHost(e.currentTarget.value)} placeholder="imap.example.org" />
        <NumberInput label="Port" value={port} onChange={setPort} min={1} max={65535} w={120} />
      </Group>
      <Switch label="Use TLS" checked={tls} onChange={(e) => setTLS(e.currentTarget.checked)} />
      <Group grow align="flex-start">
        <TextInput label="User" value={user} onChange={(e) => setUser(e.currentTarget.value)} autoComplete="off" />
        <PasswordInput
          label="Password"
          value={password}
          onChange={(e) => setPassword(e.currentTarget.value)}
          autoComplete="new-password"
          description={initial.imap_password_set ? 'Saved. Leave the mask as it is to keep it.' : undefined}
        />
      </Group>
      <Group align="flex-end" wrap="wrap">
        <Autocomplete
          label="Mailbox"
          value={mailbox}
          onChange={setMailbox}
          data={mailboxes.length ? mailboxes : ['INBOX']}
          style={{ flex: '1 1 200px' }}
        />
        <Button variant="default" onClick={() => loadMailboxes.mutate()} loading={loadMailboxes.isPending}>
          List mailboxes
        </Button>
      </Group>
      <NumberInput
        label="Check every (seconds)"
        description="Between 30 and 3600 seconds."
        value={poll}
        onChange={setPoll}
        min={30}
        max={3600}
        w={220}
      />

      {testResult && (
        <Alert color={testResult.ok ? 'prussian' : 'oxide'} variant="light" withCloseButton onClose={() => setTestResult(null)}>
          {testResult.message}
        </Alert>
      )}

      <Group justify="space-between">
        <Button variant="default" onClick={() => test.mutate()} loading={test.isPending}>
          Test connection
        </Button>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save mail settings
        </Button>
      </Group>
      <Text size="xs" c="dimmed">
        Test and mailbox lookups use the saved settings. Save first.
      </Text>
    </Stack>
  )
}
