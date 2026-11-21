import { Navigate, useSearchParams } from 'react-router-dom'
import { Stack, Tabs, Title, Text } from '@mantine/core'
import { useAuth } from '../auth/context'
import { CatalogSettings } from './admin/Catalog'
import { MailSettings } from './admin/Mail'
import { OCRSettingsPanel } from './admin/OCR'
import { PaperlessPanel } from './admin/Paperless'
import { SignInSettings } from './admin/SignIn'
import { UsersSettings } from './admin/Users'

const TABS = ['signin', 'mail', 'ocr', 'catalog', 'paperless', 'users'] as const
type TabKey = (typeof TABS)[number]

export function AdminPage() {
  const { user } = useAuth()
  const [params, setParams] = useSearchParams()

  if (user?.role !== 'admin') {
    return <Navigate to="/" replace />
  }

  const requested = params.get('tab') as TabKey | null
  const tab: TabKey = requested && (TABS as readonly string[]).includes(requested) ? requested : 'signin'

  return (
    <Stack gap="lg" p="xl" className="ax-admin">
      <div>
        <Title order={2}>Admin</Title>
        <Text c="dimmed" size="sm">
          Sign-in, mail intake, text recognition, document types, Paperless import and accounts.
        </Text>
      </div>
      <Tabs value={tab} onChange={(v) => setParams({ tab: v ?? 'signin' }, { replace: true })} keepMounted={false}>
        <Tabs.List mb="lg">
          <Tabs.Tab value="signin">Sign-in</Tabs.Tab>
          <Tabs.Tab value="mail">Mail</Tabs.Tab>
          <Tabs.Tab value="ocr">Text recognition</Tabs.Tab>
          <Tabs.Tab value="catalog">Types and tags</Tabs.Tab>
          <Tabs.Tab value="paperless">Paperless</Tabs.Tab>
          <Tabs.Tab value="users">Users</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="signin">
          <SignInSettings />
        </Tabs.Panel>
        <Tabs.Panel value="mail">
          <MailSettings />
        </Tabs.Panel>
        <Tabs.Panel value="ocr">
          <OCRSettingsPanel />
        </Tabs.Panel>
        <Tabs.Panel value="catalog">
          <CatalogSettings />
        </Tabs.Panel>
        <Tabs.Panel value="paperless">
          <PaperlessPanel />
        </Tabs.Panel>
        <Tabs.Panel value="users">
          <UsersSettings />
        </Tabs.Panel>
      </Tabs>
    </Stack>
  )
}
