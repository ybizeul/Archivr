import type { ComboboxItem, ComboboxParsedItem, OptionsFilter } from '@mantine/core'

// Ligatures and stroke letters, which Unicode normalization leaves in one piece.
// Kept in step with store.letterFold.
const letters: Record<string, string> = {
  æ: 'ae',
  Æ: 'ae',
  œ: 'oe',
  Œ: 'oe',
  ß: 'ss',
  ẞ: 'ss',
  ø: 'o',
  Ø: 'o',
  ł: 'l',
  Ł: 'l',
  đ: 'd',
  Đ: 'd',
  ð: 'd',
  Ð: 'd',
  ħ: 'h',
  Ħ: 'h',
  ŧ: 't',
  Ŧ: 't',
}

/** Lowercase text with accents, ligatures and stroke letters removed, so "Santé" matches "sante". */
export function fold(value: string): string {
  const mapped = value.replace(/[æÆœŒßẞøØłŁđĐðÐħĦŧŦ]/g, (ch) => letters[ch])
  return mapped.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase()
}

/** Option lists (folders, types, tags, correspondents) match without diacritics. */
export const optionsFilter: OptionsFilter = ({ options, search, limit }) => {
  const needle = fold(search.trim())
  const result: ComboboxParsedItem[] = []
  for (const item of options) {
    if (result.length === limit) return result
    if ('group' in item) {
      const items = optionsFilter({
        options: item.items,
        search,
        limit: limit - result.length,
      })
      if (items.length > 0) result.push({ group: item.group, items: items as ComboboxItem[] })
    } else if (fold(item.label).includes(needle)) {
      result.push(item)
    }
  }
  return result
}
