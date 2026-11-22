import { Group, Stack, Text, UnstyledButton } from '@mantine/core'
import { useTags } from '../api/queries'
import { FolderTree } from './FolderTree'

interface LibraryBrowserProps {
  folder?: number
  tag?: string
  favorites?: boolean
  onFavorites: () => void
  onFolder: (id: number | undefined) => void
  onTag: (tag: string | undefined) => void
}

/** The folder tree (Favorites on top) and tag list beside the library, also shown beside an open document. */
export function LibraryBrowser({ folder, tag = '', favorites = false, onFavorites, onFolder, onTag }: LibraryBrowserProps) {
  const tags = useTags()
  return (
    <>
      <FolderTree selected={folder} onSelect={onFolder} favoritesSelected={favorites} onSelectFavorites={onFavorites} />

      <Stack gap={6} mt="lg">
        <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
          Tags
        </Text>
        <Group gap={4}>
          {(tags.data ?? []).map((t) => (
            <UnstyledButton
              key={t.id}
              className={`ax-chip${tag === t.name ? ' active' : ''}`}
              onClick={() => onTag(tag === t.name ? undefined : t.name)}
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
    </>
  )
}
