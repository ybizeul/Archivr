import type { OCRStatus } from '../api/types'
import { getDateLocalePref } from './dateLocale'

/** First entry of the browser's language list. */
export function browserLocale(): string | undefined {
  if (typeof navigator === 'undefined') return undefined
  return navigator.languages?.[0] || navigator.language || undefined
}

/**
 * The locale every date in the app uses: the one chosen in the Date format menu, else
 * the browser's. Everything that shows or accepts a date goes through this, so they
 * all agree with each other.
 */
export function dateLocale(): string | undefined {
  return getDateLocalePref() || browserLocale()
}

/**
 * "2026-10-14" → "14 Oct 2026" in en-GB, "Oct 14, 2026" in en-US, "14 oct. 2026" in fr,
 * following the browser's language. The date is built at local noon so no timezone
 * can shift the day.
 */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return ''
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso)
  if (!m) return iso
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleDateString(dateLocale(), { day: 'numeric', month: 'short', year: 'numeric' })
}

/** Timestamps from the API are UTC; show them in the browser's zone. */
export function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(dateLocale(), { dateStyle: 'medium', timeStyle: 'short' })
}

type DatePart = 'day' | 'month' | 'year'

export interface DatePattern {
  /** Field order, e.g. ['day', 'month', 'year'] for French, ['month', 'day', 'year'] for en-US. */
  order: DatePart[]
  /** Separator between the fields: "/" for fr and en, "." for de, "-" for nl. */
  separator: string
  /** Hint for an empty field, in the same order: "dd/mm/yyyy" or "mm/dd/yyyy". */
  placeholder: string
}

/**
 * Numeric date layout for a locale, read from Intl so it needs no table of countries.
 * Latin digits and the Gregorian calendar are forced so what the user types maps to
 * the ISO date the API stores.
 */
export function datePattern(locale: string | undefined = dateLocale()): DatePattern {
  let parts: Intl.DateTimeFormatPart[]
  try {
    parts = new Intl.DateTimeFormat(locale, {
      day: '2-digit',
      month: '2-digit',
      year: 'numeric',
      numberingSystem: 'latn',
      calendar: 'gregory',
    }).formatToParts(new Date(2033, 10, 22, 12))
  } catch {
    parts = new Intl.DateTimeFormat('en-GB', { day: '2-digit', month: '2-digit', year: 'numeric' }).formatToParts(
      new Date(2033, 10, 22, 12),
    )
  }
  const order: DatePart[] = []
  let separator = ''
  for (const p of parts) {
    if (p.type === 'day' || p.type === 'month' || p.type === 'year') order.push(p.type)
    // Some locales (Arabic) wrap the separator in invisible direction marks.
    else if (p.type === 'literal' && !separator) separator = p.value.replace(/[\u200e\u200f\u061c]/g, '').trim()
  }
  if (order.length !== 3) return { order: ['day', 'month', 'year'], separator: '/', placeholder: 'dd/mm/yyyy' }
  if (!separator) separator = '/'
  const hint = { day: 'dd', month: 'mm', year: 'yyyy' }
  return { order, separator, placeholder: order.map((o) => hint[o]).join(separator) }
}

/** "2021-10-31" → "31/10/2021" (fr) or "10/31/2021" (en-US). Empty or unparseable input gives "". */
export function formatDateInput(iso: string | null | undefined, pattern: DatePattern = datePattern()): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso ?? '')
  if (!m) return ''
  const fields = { year: m[1], month: m[2], day: m[3] }
  return pattern.order.map((o) => fields[o]).join(pattern.separator)
}

/**
 * Reads what someone typed in the pattern's order, with any non-digit as separator, and
 * returns "YYYY-MM-DD". Also accepts a full ISO date. Returns null for anything that is
 * not a real calendar date (31 February, month 13, a two-digit year).
 */
export function parseDateInput(text: string, pattern: DatePattern = datePattern()): string | null {
  const tokens = text.trim().split(/\D+/).filter(Boolean)
  if (tokens.length !== 3) return null
  let fields: Record<DatePart, string>
  if (tokens[0].length === 4) {
    fields = { year: tokens[0], month: tokens[1], day: tokens[2] }
  } else {
    fields = { day: '', month: '', year: '' }
    pattern.order.forEach((o, i) => {
      fields[o] = tokens[i]
    })
  }
  if (fields.year.length !== 4 || fields.month.length > 2 || fields.day.length > 2) return null
  const y = Number(fields.year)
  const mo = Number(fields.month)
  const d = Number(fields.day)
  const check = new Date(y, mo - 1, d, 12)
  if (check.getFullYear() !== y || check.getMonth() !== mo - 1 || check.getDate() !== d) return null
  const pad = (n: number, w: number) => String(n).padStart(w, '0')
  return `${pad(y, 4)}-${pad(mo, 2)}-${pad(d, 2)}`
}

export function todayISO(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** Name shown for a folder: the system folders get their friendly names, others show their name. */
export function folderName(f: { is_system: boolean; path: string; name: string }): string {
  if (!f.is_system) return f.name
  return f.path === 'INBOX' ? 'Inbox' : f.name
}

/** Folder name for headings and pickers: friendly name for system folders, "Home / Lease" for the rest. */
export function folderTitle(f: { is_system: boolean; path: string; name: string }): string {
  return f.is_system ? folderName(f) : folderLabel(f.path)
}

/** Whether a folder is the Trash system folder, where documents go once they expire. */
export function isTrash(f: { is_system: boolean; path: string }): boolean {
  return f.is_system && f.path.toLowerCase() === 'trash'
}

/** "Home/Lease" → "Home / Lease", as shown on the filing card. */
export function folderLabel(path: string): string {
  return path.split('/').join(' / ')
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/** Document numbers are shown as "No. 10043" on the light table. */
export function accession(id: number): string {
  return String(id)
}

export const OCR_LABEL: Record<OCRStatus, string> = {
  pending: 'OCR pending',
  done: 'Text found',
  failed: 'OCR failed',
  skipped: 'OCR off',
}

export function isFileNotFound(err: unknown): boolean {
  return typeof err === 'object' && err !== null && (err as { code?: string }).code === 'FILE_MISSING'
}

export function errorMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (err instanceof Error && err.message) return err.message
  return fallback
}
