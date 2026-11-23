// Shapes returned by the Archivr API. Keep these in step with backend/internal/store and archive.

export interface FolderRef {
  id: number
  name: string
  path: string
}

export interface TypeRef {
  id: number
  name: string
  color: string
}

export type OCRStatus = 'pending' | 'done' | 'failed' | 'skipped'

export interface Document {
  id: number
  title: string
  document_date: string | null
  created_at: string
  updated_at: string
  type_id: number | null
  type: TypeRef | null
  correspondent_id: number | null
  correspondent: string
  folder_id: number
  folder: FolderRef
  tags: string[]
  filename: string
  mime: string
  size: number
  sha256: string
  source: string
  source_ref: string
  ocr_status: OCRStatus
  ocr_error: string
  /** Degrees clockwise the browser turns the document. Display only. */
  rotation: 0 | 90 | 180 | 270
  /** Last valid day, YYYY-MM-DD. The document moves to Trash after it. Null when it does not expire. */
  valid_until: string | null
  /** Whether the signed-in user marked this document as a favorite. Each user has their own list. */
  favorite: boolean
  snippet?: string
}

export interface DocumentDetail extends Document {
  text: string
}

/** A signed share link, as returned when one is created. */
export interface ShareLink {
  url: string
  token: string
  expires_at: string
}

/** What a share link shows about the document. Nothing else is exposed. */
export interface SharedDocument {
  title: string
  document_date: string | null
  filename: string
  mime: string
  size: number
  rotation: 0 | 90 | 180 | 270
  expires_at: string
}

export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface Folder {
  id: number
  parent_id: number | null
  name: string
  path: string
  is_system: boolean
  document_count: number
  child_count: number
  /** The folder is private: only you see it, with everything inside it. */
  private: boolean
  /** A folder above this one is private, which hides this one too. */
  private_inherited: boolean
  /** The signed-in user pinned this folder to their favorites. Each user has their own list. */
  favorite: boolean
}

export interface DocType {
  id: number
  name: string
  color: string
  document_count: number
}

export interface Correspondent {
  id: number
  name: string
  document_count: number
}

export interface Tag {
  id: number
  name: string
  document_count: number
}

export interface DateSuggestion {
  value: string
  label: string
  source: string
}

export interface Suggestions {
  dates: DateSuggestion[]
  titles: string[]
}

export interface InboxCount {
  count: number
  folder_id: number
}

export interface DocumentPatch {
  title?: string
  document_date?: string
  type_id?: number
  /** A type by name. One that does not exist yet is created. An empty name clears the type. */
  type?: string
  correspondent?: string
  tags?: string[]
  folder_id?: number
  rotation?: number
  /** Last valid day, YYYY-MM-DD. An empty string clears it. */
  valid_until?: string
}

export interface UserRow {
  id: number
  email: string
  name: string
  role: 'admin' | 'user'
  has_password: boolean
  created_at: string
}

export interface AuthSettings {
  oidc_enabled: boolean
  disable_local_login: boolean
  oidc_issuer: string
  oidc_client_id: string
  oidc_client_secret: string
  oidc_secret_set: boolean
  oidc_admin_group: string
  oidc_ready: boolean
  callback_url: string
}

export interface IMAPSettings {
  enabled: boolean
  imap_host: string
  imap_port: number
  imap_tls: boolean
  imap_user: string
  imap_password: string
  imap_password_set: boolean
  imap_mailbox: string
  poll_interval_seconds: number
}

export interface PaperlessSettings {
  url: string
  token: string
  token_set: boolean
  insecure_tls: boolean
}

export interface PaperlessTagRule {
  tag: string
  folder_id: number
}

export interface PaperlessMapping {
  rules: PaperlessTagRule[]
}

export interface PaperlessTag {
  name: string
  document_count: number
}

export type PaperlessState = 'idle' | 'running' | 'done' | 'failed' | 'cancelled'

export interface PaperlessProgress {
  state: PaperlessState
  total: number
  processed: number
  imported: number
  already_imported: number
  duplicates: number
  moved: number
  unsupported: number
  failed: number
  by_folder: Record<string, number>
  current: string
  problems: string[]
  error: string
  started_at: string | null
  finished_at: string | null
}

export interface OCRSettings {
  enabled: boolean
  languages: string
  timeout_seconds: number
  tesseract: boolean
  tesseract_version: string
  poppler: boolean
  installed_languages: string[]
  missing_languages: string[]
  documents_missing_ocr: number
}
