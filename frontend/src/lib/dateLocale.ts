import { useSyncExternalStore } from 'react'

// Browsers do not tell a page the system's regional setting (Chrome and Electron only
// report the UI language, which is often en-US on a machine set to France), so the
// date locale can be chosen in the app. Empty means "follow the browser's language".
const KEY = 'archivr.dateLocale'

export const DATE_LOCALES = [
  { value: 'fr-FR', label: 'Français' },
  { value: 'en-GB', label: 'English (UK)' },
  { value: 'en-US', label: 'English (US)' },
  { value: 'de-DE', label: 'Deutsch' },
] as const

const listeners = new Set<() => void>()

export function getDateLocalePref(): string {
  try {
    return localStorage.getItem(KEY) ?? ''
  } catch {
    return ''
  }
}

export function setDateLocalePref(value: string): void {
  try {
    if (value) localStorage.setItem(KEY, value)
    else localStorage.removeItem(KEY)
  } catch {
    // Storage can be blocked (private mode); the choice then lasts until reload.
  }
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  window.addEventListener('storage', listener)
  return () => {
    listeners.delete(listener)
    window.removeEventListener('storage', listener)
  }
}

/** The saved choice, re-rendering the caller when it changes (here or in another tab). */
export function useDateLocalePref(): string {
  return useSyncExternalStore(subscribe, getDateLocalePref, () => '')
}
