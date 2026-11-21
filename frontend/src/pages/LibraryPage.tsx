import { useEffect, useState } from 'react'
import { Badge, Box, Button, Collapse, Group, Pagination, Paper, Select, Stack, Table, Text, TextInput, Title, UnstyledButton, Switch, Loader } from '@mantine/core'
import { useDebouncedValue, useMediaQuery } from '@mantine/hooks'
import { IconAdjustmentsHorizontal, IconChevronDown, IconChevronUp, IconFolder, IconSearch } from '@tabler/icons-react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useDocuments, useFolders, useTags, useTypes } from '../api/queries'
import type { Document } from '../api/types'
import { DateField } from '../components/DateField'
import { FolderTree } from '../components/FolderTree'
import { Snippet } from '../components/Snippet'
import { UploadButton } from '../components/UploadButton'
import { accession, formatDate, folderLabel, OCR_LABEL } from '../lib/format'

const PAGE_SIZE = 50

const SORTS = [
  { value: '', label: 'Newest first' },
  { value: 'oldest', label: 'Oldest first' },
  { value: 'date', label: 'Document date' },
  { value: 'title', label: 'Title' },
]

const OCR_FILTERS = [
  { value: '', label: 'Any OCR state' },
  { value: 'done', label: 'Text found' },
  { value: 'pending', label: 'OCR pending' },
  { value: 'failed', label: 'OCR failed' },
  { value: 'skipped', label: 'OCR off' },
]

export function LibraryPage() {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const isMobile = useMediaQuery('(max-width: 767px)', undefined, { getInitialValueInEffect: false }) ?? false
  const [browseOpen, setBrowseOpen] = useState(false)
  const [filtersOpen, setFiltersOpen] = useState(false)

  const folder = Number(params.get('folder')) || undefined
  const q = params.get('q') ?? ''
  const type = Number(params.get('type')) || undefined
  const tag = params.get('tag') ?? ''
  const from = params.get('from') ?? ''
  const to = params.get('to') ?? ''
  const ocr = params.get('ocr') ?? ''
  const sort = params.get('sort') ?? ''
  const subtree = params.get('subtree') !== '0'
  const page = Math.max(1, Number(params.get('page')) || 1)

  // Each control writes its value to the URL, so a filtered view can be bookmarked and shared.
  const update = (changes: Record<string, string | undefined>) => {
    const next = new URLSearchParams(params)
    for (const [k, v] of Object.entries(changes)) {
      if (v === undefined || v === '') next.delete(k)
      else next.set(k, v)
    }
    if (!('page' in changes)) next.delete('page')
    setParams(next, { replace: true })
  }

  // The search box is debounced, so typing does not send a request per key.
  const [qText, setQText] = useState(q)
  const [seenQ, setSeenQ] = useState(q)
  // When the URL changes from outside (a bookmark, or the Spotlight search), follow it.
  if (q !== seenQ) {
    setSeenQ(q)
    setQText(q)
  }
  const [qDebounced] = useDebouncedValue(qText, 250)
  useEffect(() => {
    if (qDebounced !== q) update({ q: qDebounced })
    // update is rebuilt on each render; only the debounced text should trigger this.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [qDebounced])

  const docs = useDocuments({
    q: q || undefined,
    folder,
    subtree: folder !== undefined && subtree,
    type,
    tag: tag || undefined,
    from: from || undefined,
    to: to || undefined,
    ocr: ocr || undefined,
    sort: sort || (q ? 'relevance' : undefined),
    limit: PAGE_SIZE,
    offset: (page - 1) * PAGE_SIZE,
  })
  const folders = useFolders()
  const types = useTypes()
  const tags = useTags()

  const currentFolder = folders.data?.find((f) => f.id === folder)
  const title = q ? `Results for “${q}”` : currentFolder ? (currentFolder.is_system ? 'Inbox' : folderLabel(currentFolder.path)) : 'All documents'
  const activeFilters = [type, from, to, ocr, sort].filter(Boolean).length
  const pages = Math.max(1, Math.ceil((docs.data?.total ?? 0) / PAGE_SIZE))

  return (
    <Box className="ax-library">
      <Box className="ax-library-side">
        {isMobile && (
          <Button
            variant="default"
            fullWidth
            justify="space-between"
            leftSection={<IconFolder size={16} stroke={1.6} />}
            rightSection={browseOpen ? <IconChevronUp size={16} stroke={1.6} /> : <IconChevronDown size={16} stroke={1.6} />}
            onClick={() => setBrowseOpen((o) => !o)}
            aria-expanded={browseOpen}
            className="ax-collapse-toggle"
          >
            Folders: {currentFolder ? (currentFolder.is_system ? 'Inbox' : folderLabel(currentFolder.path)) : 'all'}
            {tag ? ` · #${tag}` : ''}
          </Button>
        )}
        <Collapse expanded={!isMobile || browseOpen}>
          <Box className="ax-library-side-body">
            <FolderTree selected={folder} onSelect={(id) => {
                update({ folder: id ? String(id) : undefined, page: undefined })
                setBrowseOpen(false)
              }} />

            <Stack gap={6} mt="lg">
              <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
                Tags
              </Text>
              <Group gap={4}>
                {(tags.data ?? []).map((t) => (
                  <UnstyledButton
                    key={t.id}
                    className={`ax-chip${tag === t.name ? ' active' : ''}`}
                    onClick={() => update({ tag: tag === t.name ? undefined : t.name })}
                    aria-pressed={tag === t.name}
                  >
                    {t.name} <span className="ax-mono">{t.document_count}</span>
                  </UnstyledButton>
                ))}
                {(tags.data ?? []).length === 0 && (
                  <Text size="xs" c="dimmed">
                    Tags you add to documents appear here.
                  </Text>
                )}
              </Group>
            </Stack>
          </Box>
        </Collapse>
      </Box>

      <Box className="ax-library-main">
        <Group justify="space-between" align="flex-end" wrap="nowrap" mb="md" gap="sm">
          <Title order={2} className="ax-library-title">
            {title}
          </Title>
          <Box visibleFrom="sm">
            <UploadButton variant="filled" />
          </Box>
        </Group>

        <Box className="ax-filters">
          <TextInput
            className="ax-filter-search"
            placeholder="Search text, titles, tags"
            leftSection={<IconSearch size={16} stroke={1.6} />}
            value={qText}
            onChange={(e) => setQText(e.currentTarget.value)}
            aria-label="Search documents"
            w={{ base: '100%', sm: 300 }}
            size="sm"
          />
          {isMobile && (
            <Button
              variant="default"
              size="sm"
              className="ax-filter-toggle"
              leftSection={<IconAdjustmentsHorizontal size={16} stroke={1.6} />}
              rightSection={filtersOpen ? <IconChevronUp size={16} stroke={1.6} /> : <IconChevronDown size={16} stroke={1.6} />}
              onClick={() => setFiltersOpen((o) => !o)}
              aria-expanded={filtersOpen}
            >
              Filters{activeFilters > 0 ? ` (${activeFilters})` : ''}
            </Button>
          )}
          <Collapse expanded={!isMobile || filtersOpen} className="ax-filter-collapse">
            <Box className="ax-filter-grid">
              <Select
                aria-label="Type"
                placeholder="All types"
                data={(types.data ?? []).map((t) => ({ value: String(t.id), label: t.name }))}
                value={type ? String(type) : null}
                onChange={(v) => update({ type: v ?? undefined })}
                clearable
                w={{ base: '100%', sm: 170 }}
                size="sm"
              />
              <DateField
                aria-label="Documents from"
                value={from}
                onValueChange={(v) => update({ from: v })}
                w={{ base: '100%', sm: 170 }}
                size="sm"
              />
              <DateField
                aria-label="Documents to"
                value={to}
                onValueChange={(v) => update({ to: v })}
                w={{ base: '100%', sm: 170 }}
                size="sm"
              />
              <Select
                aria-label="OCR state"
                data={OCR_FILTERS}
                value={ocr}
                onChange={(v) => update({ ocr: v ?? undefined })}
                allowDeselect={false}
                w={{ base: '100%', sm: 170 }}
                size="sm"
              />
              <Select
                aria-label="Sort"
                data={q ? [{ value: 'relevance', label: 'Best match' }, ...SORTS] : SORTS}
                value={sort || (q ? 'relevance' : '')}
                onChange={(v) => update({ sort: v ?? undefined })}
                allowDeselect={false}
                w={{ base: '100%', sm: 170 }}
                size="sm"
              />
              {folder !== undefined && (
                <Switch
                  className="ax-filter-wide"
                  label="Include subfolders"
                  checked={subtree}
                  onChange={(e) => update({ subtree: e.currentTarget.checked ? undefined : '0' })}
                  size="sm"
                />
              )}
            </Box>
          </Collapse>
        </Box>

        <Paper className="ax-table-sheet" p={0} withBorder>
          {docs.isPending ? (
            <Box p="lg">
              <Loader size="sm" />
            </Box>
          ) : docs.data?.items.length === 0 ? (
            <Box p="xl">
              <Text fw={600}>Nothing matches.</Text>
              <Text c="dimmed" size="sm">
                Try fewer filters, or search for a shorter word.
              </Text>
            </Box>
          ) : (
            isMobile ? (
              <DocumentCards items={docs.data?.items ?? []} showSnippet={q !== ''} onOpen={(id) => navigate(`/documents/${id}`)} />
            ) : (
              <DocumentTable items={docs.data?.items ?? []} showSnippet={q !== ''} onOpen={(id) => navigate(`/documents/${id}`)} />
            )
          )}
        </Paper>

        <Group justify="space-between" mt="md" gap="xs">
          <Text size="sm" c="dimmed" className="ax-mono">
            {docs.data ? `${docs.data.total} document${docs.data.total === 1 ? '' : 's'}` : ''}
          </Text>
          {pages > 1 && (
            <Pagination total={pages} value={page} onChange={(p) => update({ page: p > 1 ? String(p) : undefined })} size="sm" siblings={isMobile ? 0 : 1} />
          )}
        </Group>
      </Box>
    </Box>
  )
}

function DocumentTable({
  items,
  showSnippet,
  onOpen,
}: {
  items: Document[]
  showSnippet: boolean
  onOpen: (id: number) => void
}) {
  return (
    <Table highlightOnHover verticalSpacing="sm" horizontalSpacing="md" className="ax-doc-table" stickyHeader>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>No.</Table.Th>
          <Table.Th>Title</Table.Th>
          <Table.Th>Date</Table.Th>
          <Table.Th>Type</Table.Th>
          <Table.Th>Folder</Table.Th>
          <Table.Th>Tags</Table.Th>
          <Table.Th>OCR</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {items.map((d) => (
          <Table.Tr
            key={d.id}
            onClick={() => onOpen(d.id)}
            style={{ cursor: 'pointer' }}
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onOpen(d.id)
            }}
          >
            <Table.Td className="ax-mono">{accession(d.id)}</Table.Td>
            <Table.Td maw={380}>
              <Text size="sm" fw={600} truncate>
                {d.title}
              </Text>
              {showSnippet && d.snippet && (
                <Text size="xs" c="dimmed" lineClamp={2} className="ax-snippet">
                  <Snippet text={d.snippet} />
                </Text>
              )}
            </Table.Td>
            <Table.Td className="ax-mono">{formatDate(d.document_date)}</Table.Td>
            <Table.Td>
              {d.type ? (
                <Group gap={6} wrap="nowrap">
                  <span className="ax-type-dot" style={{ background: d.type.color }} aria-hidden="true" />
                  <Text size="sm">{d.type.name}</Text>
                </Group>
              ) : (
                <Text size="sm" c="dimmed">
                  —
                </Text>
              )}
            </Table.Td>
            <Table.Td>
              <Text size="sm" truncate maw={200}>
                {folderLabel(d.folder.path)}
              </Text>
            </Table.Td>
            <Table.Td>
              <Group gap={4} wrap="nowrap">
                {d.tags.slice(0, 3).map((t) => (
                  <Badge key={t} variant="light" color="gray" size="sm" radius="sm">
                    {t}
                  </Badge>
                ))}
              </Group>
            </Table.Td>
            <Table.Td>
              <Badge variant="outline" color={d.ocr_status === 'failed' ? 'oxide' : 'gray'} size="sm" radius="sm">
                {OCR_LABEL[d.ocr_status]}
              </Badge>
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  )
}

/** The phone layout of the document list: one card per document, so no column needs a sideways scroll. */
function DocumentCards({
  items,
  showSnippet,
  onOpen,
}: {
  items: Document[]
  showSnippet: boolean
  onOpen: (id: number) => void
}) {
  return (
    <Stack gap={0} className="ax-doc-cards" component="ul">
      {items.map((d) => (
        <li key={d.id}>
          <UnstyledButton className="ax-doc-card" onClick={() => onOpen(d.id)} aria-label={`No. ${d.id}, ${d.title}`}>
            <Group justify="space-between" gap="xs" wrap="nowrap">
              <Text size="xs" className="ax-mono ax-accent">
                {accession(d.id)}
              </Text>
              <Text size="xs" c="dimmed" className="ax-mono">
                {formatDate(d.document_date)}
              </Text>
            </Group>
            <Text size="sm" fw={600} lineClamp={2} className="ax-doc-card-title">
              {d.title}
            </Text>
            {showSnippet && d.snippet && (
              <Text size="xs" c="dimmed" lineClamp={2} className="ax-snippet">
                <Snippet text={d.snippet} />
              </Text>
            )}
            <Group gap={6} mt={4}>
              {d.type && (
                <Group gap={6} wrap="nowrap">
                  <span className="ax-type-dot" style={{ background: d.type.color }} aria-hidden="true" />
                  <Text size="xs">{d.type.name}</Text>
                </Group>
              )}
              <Text size="xs" c="dimmed" className="ax-doc-card-folder">
                {folderLabel(d.folder.path)}
              </Text>
            </Group>
            <Group gap={4} mt={4}>
              {d.tags.slice(0, 3).map((t) => (
                <Badge key={t} variant="light" color="gray" size="sm" radius="sm">
                  {t}
                </Badge>
              ))}
              {(d.ocr_status === 'failed' || d.ocr_status === 'pending') && (
                <Badge variant="outline" color={d.ocr_status === 'failed' ? 'oxide' : 'gray'} size="sm" radius="sm">
                  {OCR_LABEL[d.ocr_status]}
                </Badge>
              )}
            </Group>
          </UnstyledButton>
        </li>
      ))}
    </Stack>
  )
}
