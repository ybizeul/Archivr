import { useState } from 'react'
import { ActionIcon, Badge, Button, CopyButton, Group, Loader, PasswordInput, Stack, Switch, Text, TextInput, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import { api } from '../../api/client'
import { useAuthSettings } from '../../api/queries'
import type { AuthSettings } from '../../api/types'
import { errorMessage } from '../../lib/format'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { keys } from '../../api/queries'

const MASK = '********'

export function SignInSettings() {
  const settings = useAuthSettings()
  if (settings.isPending) return <Loader size="sm" />
  if (!settings.data) return <Text c="dimmed">The sign-in settings could not be loaded.</Text>
  return <SignInForm key={JSON.stringify(settings.data)} initial={settings.data} />
}

function SignInForm({ initial }: { initial: AuthSettings }) {
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(initial.oidc_enabled)
  const [disableLocal, setDisableLocal] = useState(initial.disable_local_login)
  const [issuer, setIssuer] = useState(initial.oidc_issuer)
  const [clientID, setClientID] = useState(initial.oidc_client_id)
  // The stored secret is never sent back. The mask means "keep what is saved".
  const [secret, setSecret] = useState(initial.oidc_secret_set ? MASK : '')
  const [group, setGroup] = useState(initial.oidc_admin_group)

  const save = useMutation({
    mutationFn: () =>
      api.put<AuthSettings>('/admin/settings/auth', {
        oidc_enabled: enabled,
        disable_local_login: disableLocal,
        oidc_issuer: issuer,
        oidc_client_id: clientID,
        oidc_client_secret: secret,
        oidc_admin_group: group,
      }),
    onSuccess: () => {
      notifications.show({ color: 'prussian', message: 'Sign-in settings saved' })
      void qc.invalidateQueries({ queryKey: keys.adminAuth })
    },
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
  })

  return (
    <Stack maw={620} gap="md">
      <Group gap="sm">
        <Text fw={600}>Single sign-on (OIDC)</Text>
        <Badge variant="outline" color={initial.oidc_ready ? 'prussian' : 'gray'} radius="sm">
          {initial.oidc_ready ? 'Ready' : 'Not complete'}
        </Badge>
      </Group>
      <Switch label="Sign in with SSO" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
      <TextInput label="Issuer" description="For example https://auth.example.org" value={issuer} onChange={(e) => setIssuer(e.currentTarget.value)} disabled={!enabled} />
      <TextInput label="Client ID" value={clientID} onChange={(e) => setClientID(e.currentTarget.value)} disabled={!enabled} />
      <PasswordInput
        label="Client secret"
        value={secret}
        onChange={(e) => setSecret(e.currentTarget.value)}
        disabled={!enabled}
        description={initial.oidc_secret_set ? 'Saved. Leave the mask as it is to keep the stored secret.' : undefined}
      />
      <TextInput
        label="Admin group"
        description="Users in this group from the identity provider become Archivr admins on sign-in."
        value={group}
        onChange={(e) => setGroup(e.currentTarget.value)}
        disabled={!enabled}
      />
      <TextInput
        label="Callback URL"
        description="Register this exact address as the redirect URI in the identity provider."
        value={initial.callback_url}
        readOnly
        rightSection={
          <CopyButton value={initial.callback_url}>
            {({ copied, copy }) => (
              <Tooltip label={copied ? 'Copied' : 'Copy'}>
                <ActionIcon variant="subtle" color="gray" onClick={copy} aria-label="Copy callback URL">
                  {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
                </ActionIcon>
              </Tooltip>
            )}
          </CopyButton>
        }
      />
      <Switch
        label="Turn off password sign-in"
        description="Only SSO works after this is on. Sign-in must be set up and tested first."
        checked={disableLocal}
        onChange={(e) => setDisableLocal(e.currentTarget.checked)}
        disabled={!enabled}
      />
      <Group justify="flex-end">
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save sign-in
        </Button>
      </Group>
    </Stack>
  )
}
