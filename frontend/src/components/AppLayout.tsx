import { useState } from 'react'
import { ActionIcon, AppShell, Badge, Box, Button, Group, Menu, Text, TextInput, Tooltip } from '@mantine/core'
import { useDebouncedValue, useMediaQuery } from '@mantine/hooks'
import { Spotlight, spotlight, type SpotlightActionData } from '@mantine/spotlight'
import { IconCalendar, IconCheck, IconFolders, IconInbox, IconLogout, IconSearch, IconSettings } from '@tabler/icons-react'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/context'
import { useDocuments, useInboxCount } from '../api/queries'
import { DATE_LOCALES, setDateLocalePref, useDateLocalePref } from '../lib/dateLocale'
import { browserLocale, datePattern, folderLabel, formatDate, formatDateInput } from '../lib/format'
import { UploadButton } from './UploadButton'

export function AppLayout() {
  const { user, logout } = useAuth()
  const { pathname } = useLocation()
  const inbox = useInboxCount()
  const navigate = useNavigate()
  const dateLocalePref = useDateLocalePref()
  const isMobile = useMediaQuery('(max-width: 767px)', undefined, { getInitialValueInEffect: false }) ?? false

  // With SSO, end the provider session too. Otherwise just go to the login page.
  const signOut = async () => {
    const providerLogout = await logout()
    if (providerLogout) window.location.assign(providerLogout)
    else navigate('/login')
  }

  const nav = [
    { to: '/inbox', label: 'Inbox', icon: <IconInbox size={22} stroke={1.6} />, active: pathname === '/inbox', count: inbox.data?.count },
    { to: '/library', label: 'Library', icon: <IconFolders size={22} stroke={1.6} />, active: pathname.startsWith('/library') || pathname.startsWith('/documents') },
    ...(user?.role === 'admin' ? [{ to: '/admin', label: 'Admin', icon: <IconSettings size={22} stroke={1.6} />, active: pathname.startsWith('/admin') }] : []),
  ]

  return (
    <AppShell header={{ height: 56 }} footer={{ height: 56, collapsed: !isMobile }} padding={0}>
      <AppShell.Header>
        <Box className="ax-topbar">
          <Group gap="lg" wrap="nowrap" className="ax-topbar-side">
            <Text component={Link} to="/library" fw={700} size="lg" c="prussian.6" className="ax-brand">
              Archivr
            </Text>
            <Group gap={4} wrap="nowrap" component="nav" aria-label="Main" visibleFrom="sm">
              {nav.map((item) => (
                <Button
                  key={item.to}
                  component={Link}
                  to={item.to}
                  variant={item.active ? 'light' : 'subtle'}
                  color={item.active ? 'prussian' : 'gray'}
                  size="xs"
                  aria-current={item.active ? 'page' : undefined}
                  rightSection={
                    item.count ? (
                      <Badge size="xs" color="carmine" variant="filled" radius="sm" className="ax-mono">
                        {item.count}
                      </Badge>
                    ) : undefined
                  }
                >
                  {item.label}
                </Button>
              ))}
            </Group>
          </Group>

          <Box className="ax-topbar-search" visibleFrom="sm">
            <Tooltip label="Search (Ctrl/⌘ K)">
              <TextInput
                classNames={{ root: 'ax-global-search', wrapper: 'ax-global-search-field' }}
                placeholder="Search"
                aria-label="Search"
                radius={999}
                size="sm"
                pointer
                readOnly
                leftSection={<IconSearch size={16} stroke={1.6} />}
                onClick={() => spotlight.open()}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') spotlight.open()
                }}
              />
            </Tooltip>
          </Box>

          <Group gap={6} wrap="nowrap" justify="flex-end" className="ax-topbar-side">
            <Box hiddenFrom="sm">
              <Tooltip label="Search (Ctrl/⌘ K)">
                <ActionIcon variant="default" size="lg" aria-label="Search" onClick={() => spotlight.open()}>
                  <IconSearch size={18} stroke={1.6} />
                </ActionIcon>
              </Tooltip>
            </Box>
            <Box visibleFrom="sm">
              <UploadButton size="xs" />
            </Box>
            <Box hiddenFrom="sm">
              <UploadButton compact />
            </Box>
            <DateFormatMenu />
            <Text size="sm" c="dimmed" truncate maw={180} visibleFrom="md">
              {user?.name || user?.email}
            </Text>
            <Tooltip label="Sign out">
              <ActionIcon variant="subtle" color="gray" aria-label="Sign out" onClick={() => void signOut()}>
                <IconLogout size={18} stroke={1.6} />
              </ActionIcon>
            </Tooltip>
          </Group>
        </Box>
      </AppShell.Header>

      <AppShell.Main>
        {/* Keyed so pages redraw their dates when the date format changes. */}
        <Outlet key={dateLocalePref} />
      </AppShell.Main>

      {isMobile && (
        <AppShell.Footer className="ax-bottomnav-shell">
          <nav className="ax-bottomnav" aria-label="Main">
            {nav.map((item) => (
              <Link key={item.to} to={item.to} className={`ax-bottomnav-item${item.active ? ' active' : ''}`} aria-current={item.active ? 'page' : undefined}>
                <span className="ax-bottomnav-icon">
                  {item.icon}
                  {item.count ? (
                    <Badge size="xs" color="carmine" variant="filled" radius="sm" className="ax-mono ax-bottomnav-badge">
                      {item.count}
                    </Badge>
                  ) : null}
                </span>
                <span>{item.label}</span>
              </Link>
            ))}
          </nav>
        </AppShell.Footer>
      )}

      <ArchiveSpotlight />
    </AppShell>
  )
}

/** Picks the language and order dates use, for browsers that do not report the system region. */
function DateFormatMenu() {
  const chosen = useDateLocalePref()
  const sample = '2026-08-11'
  const options = [
    { value: '', label: 'Browser default' },
    ...DATE_LOCALES,
  ]
  return (
    <Menu position="bottom-end" width={240} withinPortal>
      <Tooltip label="Date format">
        <Menu.Target>
          <ActionIcon variant="subtle" color="gray" aria-label="Date format">
            <IconCalendar size={18} stroke={1.6} />
          </ActionIcon>
        </Menu.Target>
      </Tooltip>
      <Menu.Dropdown>
        <Menu.Label>Date format</Menu.Label>
        {options.map((o) => (
          <Menu.Item
            key={o.value}
            onClick={() => setDateLocalePref(o.value)}
            leftSection={o.value === chosen ? <IconCheck size={14} /> : <Box w={14} />}
            rightSection={
              <Text size="xs" c="dimmed" className="ax-mono">
                {formatDateInput(sample, datePattern(o.value || browserLocale()))}
              </Text>
            }
          >
            {o.label}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  )
}

/** Cmd/Ctrl+K: jump to a view, or find a document by its text, title or tags. */
function ArchiveSpotlight() {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [debounced] = useDebouncedValue(query.trim(), 200)
  const { user } = useAuth()
  const results = useDocuments({ q: debounced, limit: 8, sort: 'relevance' }, { enabled: debounced.length > 1 })

  const go = (to: string) => () => {
    navigate(to)
    spotlight.close()
  }

  const views: SpotlightActionData[] = [
    { id: 'go-inbox', label: 'Inbox', description: 'Triage new documents', group: 'Go to', onClick: go('/inbox') },
    { id: 'go-library', label: 'Library', description: 'Browse folders', group: 'Go to', onClick: go('/library') },
    ...(user?.role === 'admin'
      ? [{ id: 'go-admin', label: 'Admin', description: 'Sign-in, mail, OCR, users', group: 'Go to', onClick: go('/admin') }]
      : []),
  ]

  const docs: SpotlightActionData[] = (results.data?.items ?? []).map((d) => ({
    id: `doc-${d.id}`,
    label: `No. ${d.id} · ${d.title}`,
    description: [folderLabel(d.folder.path), formatDate(d.document_date)].filter(Boolean).join(' · '),
    group: 'Documents',
    onClick: go(`/documents/${d.id}`),
  }))

  const searchAll: SpotlightActionData[] =
    debounced.length > 1
      ? [{ id: 'search-all', label: `Search the library for “${debounced}”`, group: 'Search', onClick: go(`/library?q=${encodeURIComponent(debounced)}`) }]
      : []

  const actions = [...docs, ...searchAll, ...views]

  return (
    <Spotlight
      actions={actions}
      // Documents come from the server, so only the navigation entries are filtered here.
      filter={(q, acts) => {
        const needle = q.trim().toLowerCase()
        return acts.filter((a) => {
          if (!('id' in a)) return true
          if (a.group === 'Go to') return needle === '' || (a.label ?? '').toLowerCase().includes(needle)
          return true
        })
      }}
      searchProps={{
        placeholder: 'Find a document, or go to…',
        leftSection: <IconSearch size={18} stroke={1.6} />,
        onChange: (e) => setQuery(e.currentTarget.value),
      }}
      nothingFound={debounced.length > 1 && !results.isFetching ? 'No matching documents.' : 'Type to search the archive.'}
      limit={20}
    />
  )
}