# Data model

SQLite, in `DATA_DIR/archivr.db`. Migrations are embedded in the binary from `backend/internal/db/migrations/` and applied in order, each in its own transaction. `schema_migrations` records what has run.

Connections are set to WAL mode, with foreign keys on and one open connection, so writes never contend.

## Tables

### `folders`

Mirrors the folder tree under `DATA_DIR/documents`.

| Column | Notes |
| --- | --- |
| `id` | Row ID. `1` is the INBOX system folder, created by the first migration. |
| `parent_id` | `NULL` at the top level. `ON DELETE RESTRICT`: a folder with children cannot be removed. |
| `name` | No `/`, not empty. |
| `path` | Full path such as `Home/Lease`. Unique, and unique case-insensitively (`folders_path_nocase`). |
| `is_system` | `1` for INBOX. It cannot be renamed, moved or deleted. |

### `documents`

One row per archived file.

| Column | Notes |
| --- | --- |
| `id` | The document number, shown as "No. 10000". `AUTOINCREMENT`, with the sequence seeded to `9999`, so the first issued number is 10000 and numbers are never reused. |
| `title` | Free text, up to 120 characters. Sanitised before it becomes a file name. |
| `document_date` | `YYYY-MM-DD`, or `NULL` until someone sets it. A check constraint enforces the format. |
| `type_id` | Optional. `ON DELETE RESTRICT`: a type in use cannot be removed. |
| `correspondent_id` | Who the document is from or to. Optional, `ON DELETE RESTRICT`: see `correspondents`. Searchable: its name is indexed with the tags. |
| `folder_id` | Where the file lives. Required. |
| `filename` | Name on disk, relative to its folder: `<ID> - <title>.<ext>`. |
| `mime` | From content sniffing, not from the file name. |
| `size`, `sha256` | Bytes and checksum. `sha256` is unique (`documents_sha256_unique`). |
| `source` | `upload`, `watch`, `imap` or `paperless`. |
| `source_ref` | `name\|size` for uploads and watch files, Message-ID for mail, `host#id` (the Paperless server and document ID) for Paperless. |
| `ocr_status` | `pending`, `done`, `failed` or `skipped` (no OCR engine installed). |
| `ocr_error` | Last error message from OCR, empty otherwise. |
| `rotation` | `0`, `90`, `180` or `270`: degrees clockwise the browser turns the document for display. Display metadata only, the file on disk is never rotated. Default `0`, set by the rotate buttons. |
| `created_at`, `updated_at` | UTC timestamps. |

Indexes: folder, type, OCR status, date, created.

### `document_types`

`name` is unique and case-insensitive. `color` is a free-form value the UI maps to a palette colour.

### `correspondents`

The correspondent value list. `name` is unique and case-insensitive, and names are at most 200 characters. A name typed on a document that is not in the list yet is added when the document is saved, in the same transaction. Migration `0005` built the list from the names that were free text before.

### `tags`, `document_tags`

Tags are unique and case-insensitive. Links are cascade-deleted with the document or tag.

### `document_content`

One row per document, written by the OCR worker.

| Column | Notes |
| --- | --- |
| `body` | Extracted text. Pages are joined with a blank line. |
| `engine` | Recorded as `tesseract` for every document today, including PDFs read from their text layer. |
| `languages` | Tesseract language codes used, such as `eng+fra`. |

### `documents_fts`

FTS5 virtual table over `title`, `body`, `tags`, `type_name`, with `document_id UNINDEXED`. Tokenised with `unicode61 remove_diacritics 2`, so `Café` matches `cafe`.

The store layer keeps it in step with the document rows, inside the same transaction as each change. Nothing else writes to it.

### `users`

| Column | Notes |
| --- | --- |
| `email` | Unique, case-insensitive. The sign-in identifier. |
| `role` | `admin` or `user`. |
| `password_hash` | argon2id. Empty for accounts that only sign in through SSO. |

### `settings`

Key and JSON value. Keys in use:

| Key | Contents |
| --- | --- |
| `auth` | SSO settings: enable flag, issuer, client ID and secret, admin group, `disable_local_login`. |
| `imap` | Host, port, TLS mode, user, password, mailbox, poll interval. |
| `ocr` | Languages, time limit per document, enabled flag. |

Secrets are stored here in plain text. They are masked in every API response.

### `ingest_log`

Records what has been imported, so repeated scans and polls do nothing new.

| Column | Notes |
| --- | --- |
| `source`, `source_ref`, `sha256` | Primary key. |
| `document_id` | The document it produced. Set to `NULL` when that document is deleted. A record with no document no longer counts as "already imported", so the same source can be filed again. |

## On disk

```
DATA_DIR/documents/<folder path>/<ID> - <title>.<ext>
DATA_DIR/documents/<folder path>/<ID> - <title>.<ext>
DATA_DIR/documents/<folder path>/<ID> - <title>.<ext>.archivr.json   (sidecar)
DATA_DIR/trash/<id>-<nanoseconds>-<name>                             (short-lived, see Delete)
DATA_DIR/cache/thumbs/<sha[:2]>/<sha>-480.jpg                        (previews)
```

### Sidecar

Written after each commit, and rewritten on each change. Its fields are the document's number, title, date, type name, correspondent, tags, source, source reference and checksum:

```json
{
  "id": 10000,
  "title": "Landlord letter",
  "document_date": "2026-03-04",
  "type": "Letter",
  "tags": ["lease", "2026"],
  "source": "watch",
  "source_ref": "scan-0001.pdf|482113",
  "sha256": "…"
}
```

A sidecar write that fails is logged, and the change still stands. The database is the source of truth. The sidecar is a copy kept beside the file. There is no rebuild command yet.

### Delete

Deleting moves the file and its sidecar into `trash/` inside the transaction, then removes the row. If the transaction fails, the files move back. After the commit the trashed copy is removed. **Deletion is permanent.**

## Invariants

- `documents.folder_id` always names a folder whose `path` matches the file's directory on disk.
- Every document row has its file in the folder it names, and every file in a folder has a row, except during an operation that is still in progress.
- `documents_fts` has one row per document. It is updated in the same transaction as the document.
- A folder with documents or subfolders cannot be deleted. Renaming or moving a folder moves its directory and its subtree. INBOX cannot be renamed, moved or deleted.
