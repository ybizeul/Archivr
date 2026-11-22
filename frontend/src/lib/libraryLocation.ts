import { useSyncExternalStore } from 'react'

const KEY = 'archivr-library-location'
const listeners = new Set<() => void>()

/** The Library address (folder, filters, page) the user last had open, so "Library" returns there. */
export function getLibraryLocation(): string {
  try {
    const saved = sessionStorage.getItem(KEY)
    if (saved && saved.startsWith('/library')) return saved
  } catch {
    // Storage can be unavailable (private mode); fall back to the plain library.
  }
  return '/library'
}

export function rememberLibraryLocation(search: string) {
  const next = `/library${search}`
  if (next === getLibraryLocation()) return
  try {
    sessionStorage.setItem(KEY, next)
  } catch {
    return
  }
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function useLibraryLocation(): string {
  return useSyncExternalStore(subscribe, getLibraryLocation, () => '/library')
}
