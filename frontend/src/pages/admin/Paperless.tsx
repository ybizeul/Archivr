import { useEffect, useRef, useState } from 'react'
import { ActionIcon, Alert, Autocomplete, Badge, Button, Code, Group, List, Loader, PasswordInput, Progress, ScrollArea, Select, Stack, Switch, Table, Text, TextInput, Title, Tooltip } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconArrowDown, IconArrowUp, IconCloudDownload, IconPlus, IconSearch, IconTrash } from '@tabler/icons-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { keys, useFolders, usePaperlessImport, usePaperlessMapping, usePaperlessSettings } from '../../api/queries'
import type { Folder, PaperlessMapping, PaperlessProgress, PaperlessSettings, PaperlessState, PaperlessTag } from '../../api/types'
import { NewFolder } from '../../components/NewFolder'
import { errorMessage, folderLabel, formatDateTime, isTrash } from '../../lib/format'

const MASK = '********'


/** Bring documents over from a Paperless-ngx server. Nothing is changed in Paperless. */
export function PaperlessPanel() {
  const settings = usePaperlessSettings()
  if (settings.isPending) return <Loader size="sm" />
  if (!settings.data) return <Text c="dimmed">The Paperless settings could not be loaded.</Text>
  return (
    <Stack gap="xl" maw={620}>
      <ConnectionForm key={JSON.stringify(settings.data)} initial={settings.data} />
      <ImportSection configured={settings.data.url !== '' && settings.data.token_set} />
    </Stack>
  )
}

function ConnectionForm({ initial }: { initial: PaperlessSettings }) {
  const qc = useQueryClient()
  const [url, setUrl] = useState(initial.url)
  const [token, setToken] = useState(initial.token_set ? MASK : '')
  const [insecureTLS, setInsecureTLS] = useState(initial.insecure_tls)
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null)

  const save = useMutation({
    mutationFn: () => api.put<PaperlessSettings>('/admin/settings/paperless', { url: url.trim(), token, insecure_tls: insecureTLS }),
    onSuccess: () => {
      notifications.show({ color: 'prussian', message: 'Paperless settings saved' })
      void qc.invalidateQueries({ queryKey: keys.adminPaperless })
    },
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
  })

  // The test uses the saved settings, so the form must be saved first.
  const test = useMutation({
    mutationFn: () => api.post<{ ok: boolean; documents: number }>('/admin/settings/paperless/test'),
    onSuccess: (res) =>
      setTestResult({ ok: true, message: `Connected. The token can read ${res.documents.toLocaleString()} documents.` }),
    onError: (err) => setTestResult({ ok: false, message: errorMessage(err) }),
  })

  return (
    <Stack gap="md">
      <div>
        <Title order={4}>Connection</Title>
        <Text size="sm" c="dimmed">
          The address of your Paperless-ngx server and an API token. Create the token in Paperless under My Profile. The
          token needs to read documents only.
        </Text>
      </div>
      <TextInput
        label="Address"
        value={url}
        onChange={(e) => setUrl(e.currentTarget.value)}
        placeholder="https://paperless.example.org"
        autoComplete="off"
      />
      <PasswordInput
        label="API token"
        value={token}
        onChange={(e) => setToken(e.currentTarget.value)}
        autoComplete="new-password"
        description={initial.token_set ? 'Saved. Leave the mask as it is to keep it.' : undefined}
      />
      <Switch
        label="Ignore SSL certificate errors"
        description="For a server with a self-signed or internal certificate. The certificate is not checked at all, so anyone on the network path could read the token. Use it only on a network you trust."
        checked={insecureTLS}
        onChange={(e) => setInsecureTLS(e.currentTarget.checked)}
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
          Save connection
        </Button>
      </Group>
      <Text size="xs" c="dimmed">
        Testing and importing use the saved settings. Save first.
      </Text>
    </Stack>
  )
}

const STATE_LABEL: Record<PaperlessState, string> = {
  idle: 'Not run yet',
  running: 'Importing',
  done: 'Finished',
  failed: 'Stopped by an error',
  cancelled: 'Cancelled',
}

const STATE_COLOR: Record<PaperlessState, string> = {
  idle: 'gray',
  running: 'prussian',
  done: 'green',
  failed: 'red',
  cancelled: 'gray',
}

/** A rule being edited. The folder is empty until one is picked. */
interface DraftRule {
  tag: string
  folder: string | null
}

function ImportSection({ configured }: { configured: boolean }) {
  const mapping = usePaperlessMapping()
  const folders = useFolders()
  if (mapping.isPending || folders.isPending) return <Loader size="sm" />
  if (!mapping.data || !folders.data) return <Text c="dimmed">The import settings could not be loaded.</Text>
  return <ImportForm configured={configured} saved={mapping.data} folders={folders.data} />
}

function toDraft(m: PaperlessMapping): DraftRule[] {
  return m.rules.map((r) => ({ tag: r.tag, folder: String(r.folder_id) }))
}

function ImportForm({ configured, saved, folders }: { configured: boolean; saved: PaperlessMapping; folders: Folder[] }) {
  const qc = useQueryClient()
  const progress = usePaperlessImport()

  // Tags are read from Paperless on request, not on every visit to this screen.
  const getTags = useMutation({
    mutationFn: () => api.get<{ items: PaperlessTag[] }>('/admin/paperless/tags').then((r) => r.items),
  })
  const [folderId, setFolderId] = useState<string>('1')
  const [rules, setRules] = useState<DraftRule[]>(() => toDraft(saved))

  const state = progress.data?.state
  const running = state === 'running'

  // When a run ends, whatever it filed should show up in the rest of the app.
  const previous = useRef<PaperlessState | undefined>(undefined)
  useEffect(() => {
    if (previous.current === 'running' && state && state !== 'running') {
      for (const key of [['documents'], ['inbox'], ['folders'], ['types'], ['tags']]) {
        void qc.invalidateQueries({ queryKey: key })
      }
    }
    previous.current = state
  }, [state, qc])

  const folderIds = new Set(folders.map((f) => String(f.id)))
  const complete = (r: DraftRule) => r.tag.trim() !== '' && r.folder !== null && folderIds.has(r.folder)
  const allComplete = rules.every(complete)
  const body = (): PaperlessMapping => ({
    rules: rules.map((r) => ({ tag: r.tag.trim(), folder_id: Number(r.folder) })),
  })
  const dirty = JSON.stringify(body().rules) !== JSON.stringify(saved.rules)

  const saveMapping = useMutation({
    mutationFn: () => api.put<PaperlessMapping>('/admin/paperless/mapping', body()),
    onSuccess: (res) => qc.setQueryData(keys.adminPaperlessMapping, res),
  })

  // Starting saves the rules first, so what is on screen is what the import uses.
  const start = useMutation({
    mutationFn: async () => {
      if (dirty) await saveMapping.mutateAsync()
      return api.post<PaperlessProgress>('/admin/paperless/import', {
        folder_id: Number(folderId) || 0,
      })
    },
    onSuccess: (res) => qc.setQueryData(keys.adminPaperlessImport, res),
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't start the import", message: errorMessage(err) }),
  })

  const cancel = useMutation({
    mutationFn: () => api.post<PaperlessProgress>('/admin/paperless/import/cancel'),
    onSuccess: (res) => qc.setQueryData(keys.adminPaperlessImport, res),
  })

  const folderOptions = folders
    .filter((f) => !isTrash(f))
    .map((f) => ({
      value: String(f.id),
      label: f.is_system ? 'Inbox (sort them with triage)' : folderLabel(f.path),
    }))

  return (
    <Stack gap="md">
      <div>
        <Title order={4}>Import</Title>
        <Text size="sm" c="dimmed">
          Each document keeps its title, date, document type and tags. Archivr reads the files again with its own text
          recognition, so large libraries take a while to become searchable. Running the import again only adds what is
          new, and a document you deleted in Archivr is brought back. Documents already in the archive are not imported again, but the tag rules below still move them.
        </Text>
      </div>

      <Group align="flex-end" gap="xs" wrap="nowrap">
        <Select
          label="File documents into"
          description="Documents that match none of the rules below go here."
          data={folderOptions}
          value={folderId}
          onChange={(v) => setFolderId(v ?? '1')}
          allowDeselect={false}
          searchable
          disabled={running}
          style={{ flex: 1 }}
        />
        <NewFolder compact disabled={running} onCreated={(f) => setFolderId(String(f.id))} />
      </Group>

      <RuleEditor
        rules={rules}
        onChange={setRules}
        folders={folders}
        tags={getTags.data}
        tagsError={getTags.isError ? errorMessage(getTags.error) : null}
        onGetTags={() => getTags.mutate()}
        gettingTags={getTags.isPending}
        canGetTags={configured}
        disabled={running}
      />


      <Group>
        {running ? (
          <Button color="red" variant="light" onClick={() => cancel.mutate()} loading={cancel.isPending}>
            Cancel import
          </Button>
        ) : (
          <>
            <Button onClick={() => start.mutate()} loading={start.isPending} disabled={!configured || !allComplete}>
              Start import
            </Button>
            <Button
              variant="default"
              onClick={() =>
                saveMapping.mutate(undefined, {
                  onSuccess: () => notifications.show({ color: 'prussian', message: 'Rules saved' }),
                  onError: (err) => notifications.show({ color: 'red', title: "Couldn't save the rules", message: errorMessage(err) }),
                })
              }
              loading={saveMapping.isPending && !start.isPending}
              disabled={!dirty || !allComplete}
            >
              Save rules
            </Button>
          </>
        )}
        {!configured && (
          <Text size="sm" c="dimmed">
            Save the connection first.
          </Text>
        )}
        {configured && !allComplete && (
          <Text size="sm" c="dimmed">
            Finish or remove the incomplete rules.
          </Text>
        )}
      </Group>

      {progress.data && progress.data.state !== 'idle' && <ProgressView p={progress.data} />}
    </Stack>
  )
}

function RuleEditor({
  rules,
  onChange,
  folders,
  tags,
  tagsError,
  onGetTags,
  gettingTags,
  canGetTags,
  disabled,
}: {
  rules: DraftRule[]
  onChange: (rules: DraftRule[]) => void
  folders: Folder[]
  tags: PaperlessTag[] | undefined
  tagsError: string | null
  onGetTags: () => void
  gettingTags: boolean
  canGetTags: boolean
  disabled: boolean
}) {
  const counts = new Map((tags ?? []).map((t) => [t.name, t.document_count]))
  const folderData = folders
    .filter((f) => !f.is_system)
    .map((f) => ({ value: String(f.id), label: folderLabel(f.path) }))
  const folderIds = new Set(folderData.map((f) => f.value))
  const known = new Set((tags ?? []).map((t) => t.name.toLowerCase()))

  const patch = (i: number, change: Partial<DraftRule>) =>
    onChange(rules.map((r, j) => (j === i ? { ...r, ...change } : r)))
  const move = (i: number, by: -1 | 1) => {
    const next = [...rules]
    const [item] = next.splice(i, 1)
    next.splice(i + by, 0, item)
    onChange(next)
  }
  const seen = new Set<string>()
  const duplicate = rules.map((r) => {
    const key = r.tag.trim().toLowerCase()
    if (!key) return false
    const dup = seen.has(key)
    seen.add(key)
    return dup
  })

  return (
    <Stack gap="xs">
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <Text fw={500} size="sm">
          File by tag
        </Text>
        <Group gap="xs" wrap="wrap">
          <NewFolder disabled={disabled} onCreated={() => {}} />
          <Tooltip label="Save the connection first" disabled={canGetTags}>
            <Button
              variant="default"
              size="xs"
              leftSection={<IconCloudDownload size={14} />}
              onClick={onGetTags}
              loading={gettingTags}
              disabled={!canGetTags || disabled}
            >
              Get tag lists
            </Button>
          </Tooltip>
        </Group>
      </Group>
      <div>
        <Text size="xs" c="dimmed">
          A document goes to the folder of the first rule whose tag it carries, so the order is the priority. Tags are
          matched without regard to case. Rules also apply to documents that are already in Archivr, wherever they are filed now. A document that matches no rule stays where it is.
        </Text>
      </div>
      {tagsError && (
        <Alert color="oxide" variant="light">
          Could not read the tags from Paperless: {tagsError}. You can still type tag names.
        </Alert>
      )}
      {tags && (
        <TagTable
          tags={tags}
          rules={rules}
          onChange={onChange}
          folderData={folderData}
          disabled={disabled}
        />
      )}
      {rules.length > 0 && (
        <Text size="xs" fw={500} c="dimmed">
          Rules, in priority order
        </Text>
      )}
      {rules.map((r, i) => (
        <Group key={i} gap="xs" align="flex-start" wrap="nowrap" className="ax-rule-row">
          <Autocomplete
            aria-label={`Tag for rule ${i + 1}`}
            placeholder="Paperless tag"
            value={r.tag}
            onChange={(v) => patch(i, { tag: v })}
            data={(tags ?? []).map((t) => t.name)}
            renderOption={({ option }) => (
              <Group justify="space-between" w="100%" wrap="nowrap">
                <Text size="sm">{option.value}</Text>
                <Text size="xs" c="dimmed">
                  {(counts.get(option.value) ?? 0).toLocaleString()}
                </Text>
              </Group>
            )}
            error={duplicate[i] ? 'Already used by an earlier rule' : undefined}
            description={tags && r.tag.trim() !== '' && !known.has(r.tag.trim().toLowerCase()) ? 'No tag with this name in Paperless' : undefined}
            disabled={disabled}
            style={{ flex: 1 }}
          />
          <Select
            aria-label={`Folder for rule ${i + 1}`}
            placeholder="Folder"
            data={folderData}
            value={r.folder}
            onChange={(v) => patch(i, { folder: v })}
            searchable
            allowDeselect={false}
            error={r.folder !== null && !folderIds.has(r.folder) ? 'This folder no longer exists' : undefined}
            disabled={disabled}
            style={{ flex: 1 }}
          />
          <Group mt={4}>
            <NewFolder compact disabled={disabled} onCreated={(f) => patch(i, { folder: String(f.id) })} />
          </Group>
          <Group gap={2} wrap="nowrap" mt={4}>
            <Tooltip label="Higher priority">
              <ActionIcon variant="subtle" color="gray" aria-label="Move up" disabled={disabled || i === 0} onClick={() => move(i, -1)}>
                <IconArrowUp size={16} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label="Lower priority">
              <ActionIcon
                variant="subtle"
                color="gray"
                aria-label="Move down"
                disabled={disabled || i === rules.length - 1}
                onClick={() => move(i, 1)}
              >
                <IconArrowDown size={16} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label="Remove rule">
              <ActionIcon
                variant="subtle"
                color="red"
                aria-label="Remove rule"
                disabled={disabled}
                onClick={() => onChange(rules.filter((_, j) => j !== i))}
              >
                <IconTrash size={16} />
              </ActionIcon>
            </Tooltip>
          </Group>
        </Group>
      ))}
      <Group>
        <Button
          variant="default"
          size="xs"
          leftSection={<IconPlus size={14} />}
          disabled={disabled}
          onClick={() => onChange([...rules, { tag: '', folder: null }])}
        >
          Add rule
        </Button>
        {folderData.length === 0 && (
          <Text size="xs" c="dimmed">
            Create folders in the library first. Rules can only file into existing folders.
          </Text>
        )}
      </Group>
    </Stack>
  )
}

/**
 * Every tag from Paperless with a folder to file it into. Choosing a folder adds or
 * changes the rule for that tag, and clearing it removes the rule. New rules go to
 * the end of the list, so tags already mapped keep their priority.
 */
function TagTable({
  tags,
  rules,
  onChange,
  folderData,
  disabled,
}: {
  tags: PaperlessTag[]
  rules: DraftRule[]
  onChange: (rules: DraftRule[]) => void
  folderData: { value: string; label: string }[]
  disabled: boolean
}) {
  const [filter, setFilter] = useState('')
  const [onlyUnmapped, setOnlyUnmapped] = useState(false)

  const ruleIndex = (name: string) => rules.findIndex((r) => r.tag.trim().toLowerCase() === name.toLowerCase())
  const isMapped = (name: string) => {
    const i = ruleIndex(name)
    return i >= 0 && rules[i].folder !== null
  }
  const mapped = tags.filter((t) => isMapped(t.name)).length

  const setFolder = (name: string, folder: string | null) => {
    const i = ruleIndex(name)
    if (folder === null) {
      if (i >= 0) onChange(rules.filter((_, j) => j !== i))
    } else if (i >= 0) {
      onChange(rules.map((r, j) => (j === i ? { ...r, folder } : r)))
    } else {
      onChange([...rules, { tag: name, folder }])
    }
  }

  const needle = filter.trim().toLowerCase()
  const shown = tags.filter(
    (t) => (!needle || t.name.toLowerCase().includes(needle)) && (!onlyUnmapped || !isMapped(t.name)),
  )

  if (tags.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        There are no tags in Paperless.
      </Text>
    )
  }

  return (
    <Stack gap="xs" p="sm" style={{ border: '1px solid var(--mantine-color-default-border)', borderRadius: 'var(--mantine-radius-sm)' }}>
      <Group justify="space-between" wrap="wrap">
        <Text size="sm" fw={500}>
          Tags in Paperless
          <Text span size="sm" c="dimmed">
            {' '}
            · {mapped} of {tags.length} mapped
          </Text>
        </Text>
        <Switch
          size="xs"
          label="Only unmapped"
          checked={onlyUnmapped}
          onChange={(e) => setOnlyUnmapped(e.currentTarget.checked)}
        />
      </Group>
      <TextInput
        size="xs"
        placeholder="Filter tags"
        aria-label="Filter tags"
        leftSection={<IconSearch size={14} />}
        value={filter}
        onChange={(e) => setFilter(e.currentTarget.value)}
      />
      <ScrollArea.Autosize mah={320}>
        <Table verticalSpacing={4} stickyHeader className="ax-stack-table">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Tag</Table.Th>
              <Table.Th ta="right">Documents</Table.Th>
              <Table.Th w="45%">Folder</Table.Th>
              <Table.Th w={36} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {shown.map((t) => {
              const i = ruleIndex(t.name)
              const folder = i >= 0 ? rules[i].folder : null
              return (
                <Table.Tr key={t.name}>
                  <Table.Td data-label="Tag" className="ax-wrap-cell">{t.name}</Table.Td>
                  <Table.Td ta="right" data-label="Documents">
                    <Text size="sm" c="dimmed">
                      {t.document_count.toLocaleString()}
                    </Text>
                  </Table.Td>
                  <Table.Td data-label="Folder">
                    <Select
                      size="xs"
                      aria-label={`Folder for tag ${t.name}`}
                      placeholder="Not mapped"
                      data={folderData}
                      value={folder}
                      onChange={(v) => setFolder(t.name, v)}
                      clearable
                      searchable
                      disabled={disabled}
                    />
                  </Table.Td>
                  <Table.Td className="ax-actions-cell">
                    <NewFolder
                      compact
                      defaultName={t.name}
                      disabled={disabled}
                      onCreated={(f) => setFolder(t.name, String(f.id))}
                    />
                  </Table.Td>
                </Table.Tr>
              )
            })}
            {shown.length === 0 && (
              <Table.Tr>
                <Table.Td colSpan={4}>
                  <Text size="sm" c="dimmed">
                    No tags match.
                  </Text>
                </Table.Td>
              </Table.Tr>
            )}
          </Table.Tbody>
        </Table>
      </ScrollArea.Autosize>
    </Stack>
  )
}

function ProgressView({ p }: { p: PaperlessProgress }) {
  const percent = p.total > 0 ? Math.min(100, Math.round((p.processed / p.total) * 100)) : 0
  const running = p.state === 'running'
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Badge color={STATE_COLOR[p.state]} variant="light">
          {STATE_LABEL[p.state]}
        </Badge>
        <Text size="sm" c="dimmed">
          {p.processed.toLocaleString()} of {p.total.toLocaleString()}
          {p.finished_at ? ` · ${formatDateTime(p.finished_at)}` : ''}
        </Text>
      </Group>
      <Progress value={percent} animated={running} color={STATE_COLOR[p.state]} />
      {running && p.current && (
        <Text size="sm" c="dimmed" truncate>
          {p.current}
        </Text>
      )}
      {p.error && (
        <Alert color="oxide" variant="light" title="The import stopped">
          {p.error}
        </Alert>
      )}
      <Group gap="lg">
        <Count label="Imported" value={p.imported} />
        <Count label="Already imported" value={p.already_imported} />
        <Count label="Duplicates" value={p.duplicates} />
        <Count label="Moved by rules" value={p.moved} />
        <Count label="Not supported" value={p.unsupported} />
        <Count label="Failed" value={p.failed} />
      </Group>
      {Object.keys(p.by_folder ?? {}).length > 0 && (
        <Stack gap={2}>
          <Text size="sm" fw={600}>
            Filed into
          </Text>
          {Object.entries(p.by_folder)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([path, n]) => (
              <Group key={path} justify="space-between" maw={420}>
                <Text size="sm">{path === 'INBOX' ? 'Inbox' : folderLabel(path)}</Text>
                <Text size="sm" c="dimmed">
                  {n.toLocaleString()}
                </Text>
              </Group>
            ))}
        </Stack>
      )}
      {p.problems.length > 0 && (
        <Stack gap={4}>
          <Text size="sm" fw={600}>
            Not imported
            {p.unsupported + p.failed > p.problems.length ? ` (latest ${p.problems.length})` : ''}
          </Text>
          <List size="sm" spacing={2}>
            {p.problems.map((msg, i) => (
              <List.Item key={`${i}-${msg}`}>
                <Code>{msg}</Code>
              </List.Item>
            ))}
          </List>
        </Stack>
      )}
    </Stack>
  )
}

function Count({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <Text fw={600} size="lg" lh={1.1}>
        {value.toLocaleString()}
      </Text>
      <Text size="xs" c="dimmed">
        {label}
      </Text>
    </div>
  )
}
