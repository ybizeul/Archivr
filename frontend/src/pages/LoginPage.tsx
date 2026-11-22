import { useEffect, useState, type FormEvent } from 'react'
import { Alert, Button, Center, Divider, Loader, Paper, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { Navigate, useLocation } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import { useAuth } from '../auth/context'

interface OIDCStatus {
  enabled: boolean
  disable_local_login: boolean
}

// Messages for the codes the server can send back. Errors say what happened and what to do.
const OIDC_ERRORS: Record<string, string> = {
  OIDC_STATE_INVALID: 'That sign-in link expired or was already used. Start sign-in again.',
  OIDC_NONCE_MISMATCH: 'The sign-in response did not match this attempt. Start sign-in again.',
  OIDC_EMAIL_REQUIRED: 'Your SSO account has no email address. Ask an admin to add one.',
  OIDC_EMAIL_UNVERIFIED: 'Your SSO email address is not verified. Verify it, then sign in again.',
  OIDC_DISABLED: 'SSO is not set up. Sign in with your email and password.',
  DEFAULT: 'SSO sign-in failed. Try again, or sign in with your email and password.',
}

export function LoginPage() {
  const { user, login } = useAuth()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? '/library'

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  // The OIDC callback sends failures back here with a stable code, not a message.
  const oidcCode = new URLSearchParams(location.search).get('oidc_error')
  const [error, setError] = useState<string | null>(oidcCode ? (OIDC_ERRORS[oidcCode] ?? OIDC_ERRORS.DEFAULT) : null)
  const [submitting, setSubmitting] = useState(false)

  const oidc = useQuery({
    queryKey: ['auth', 'oidc-status'],
    queryFn: () => api.get<OIDCStatus>('/auth/oidc/status'),
  })

  const localDisabled = oidc.data?.disable_local_login === true
  // With local sign-in off, SSO is the only way in, so skip the login page. If the provider just sent us
  // back with an error, stay here and show it. Redirecting again would loop.
  const autoSSO = !user && oidc.data?.enabled === true && localDisabled && !oidcCode

  useEffect(() => {
    if (autoSSO) window.location.assign(`/api/v1/auth/oidc/start?redirect=${encodeURIComponent(from)}`)
  }, [autoSSO, from])

  if (user) return <Navigate to={from} replace />

  if (autoSSO) {
    return (
      <Center mih="100vh" p="md">
        <Loader aria-label="Redirecting to your sign-in provider" />
      </Center>
    )
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await login(email.trim(), password)
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 401
          ? 'Email or password is wrong. Check both and try again.'
          : err instanceof Error
            ? err.message
            : 'Sign-in failed. Try again.',
      )
    } finally {
      setSubmitting(false)
    }
  }

  const startSSO = () => {
    window.location.assign(`/api/v1/auth/oidc/start?redirect=${encodeURIComponent(from)}`)
  }

  return (
    <Center mih="100vh" p="md">
      <Paper w="100%" maw={380} p="xl" withBorder>
        <Stack gap="lg">
          <Stack gap={4}>
            <Title order={2}>Sign in to Archivr</Title>
            <Text size="sm" c="dimmed">
              Your documents, filed and searchable.
            </Text>
          </Stack>

          {error && (
            <Alert color="oxide" variant="light" role="alert">
              {error}
            </Alert>
          )}

          {oidc.data?.enabled && (
            <>
              <Button fullWidth size="md" onClick={startSSO}>
                Sign in with SSO
              </Button>
              {!localDisabled && <Divider label="or" labelPosition="center" />}
            </>
          )}

          {!localDisabled && (
            <form onSubmit={onSubmit}>
              <Stack gap="md">
                <TextInput
                  label="Email"
                  type="email"
                  autoComplete="username"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.currentTarget.value)}
                />
                <PasswordInput
                  label="Password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.currentTarget.value)}
                />
                <Button type="submit" variant={oidc.data?.enabled ? 'default' : 'filled'} fullWidth loading={submitting}>
                  Sign in
                </Button>
              </Stack>
            </form>
          )}
        </Stack>
      </Paper>
    </Center>
  )
}
