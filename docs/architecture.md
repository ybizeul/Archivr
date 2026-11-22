# Architecture

Archivr is one Go process. It serves the API, the embedded web app, the OCR workers, and the two intake loops (watch folder and mail). State lives in one SQLite file and one directory tree.

```
          ┌───────────── intake ─────────────┐
 scanner ─▶ watch folder ─┐                   │
 email   ─▶ IMAP poller  ─┼─▶ ingest.Importer ─┼─▶ archive.Service ─▶ storage (files + sidecars)
 browser ─▶ POST /documents┘   (sniff, dedupe) │        │                    │
                                              │        ▼                    │
                                              │   store (SQLite, FTS5) ◀────┘
                                              │        ▲
                                              │   ocr.Pool (workers) ──▶ tesseract / pdftoppm
                                              └───────────────────────────────────────────────
 browser ◀──── api (/api/v1) ◀──── session cookie ◀──── SPA (embedded)
```

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/archivr` | Wiring, startup checks, graceful shutdown, the embedded web app. |
| `internal/config` | Environment variables and their defaults. |
| `internal/db` | SQLite open (WAL, foreign keys, one writer) and embedded migrations. |
| `internal/store` | SQL only. Documents, folders, types, tags, users, settings, search. |
| `internal/storage` | Paths, safe names, atomic writes, moves with sidecars. Nothing about the database. |
| `internal/archive` | Business rules for mutations. Each one orders the file change and the database change. |
| `internal/ingest` | MIME sniffing, the importer (dedupe), the watch folder, the IMAP poller. |
| `internal/paperless` | Imports a Paperless-ngx library: the REST client, saved settings, and a background runner with progress. It files through `ingest.Importer`. |
| `internal/ocr` | Engine detection, text extraction, the worker pool, requeue of pending work. |
| `internal/preview` | Thumbnails, cached by content hash. |
| `internal/suggest` | Dates and titles proposed from OCR text. Suggestions only; the user decides. |
| `internal/auth` | Local passwords (argon2id), session tokens, OIDC flow, login rate limiting. |
| `internal/api` | HTTP handlers, the route table, and the error format. |

## Mutation rules

A file move and a database commit cannot be made atomic together, so every change follows a fixed order. The database is the source of truth, and the file operation comes last:

1. Open a database transaction, validate, compute the old and new paths, and make all the row changes (fields, tags, search index).
2. Do the file operation as the last step inside the transaction (move a document, move a folder directory, or move a deleted document into `trash/`). Creating a document is the exception: its file is written first, because the ID is only known once the row exists, and it is removed again if the transaction fails.
3. Commit. Only `Commit` can fail after the file has moved. If it does, the move is undone. A failed undo is logged at error level with the document ID and both paths, never discarded.
4. After commit, write the sidecar and queue any OCR work. A sidecar failure is logged, not fatal, because the database is the source of truth and sidecars are for recovery.

Moves carry the sidecar with them. A database failure before the move leaves the file untouched, and a test covers this.

Deleting a document deletes its rows, then moves its file into `trash/`, then commits. After the commit the trashed file and its sidecar are removed. **That removal is permanent.** `DATA_DIR/trash/` is a holding area for rollback, not a recovery bin. The Trash folder in the library is the recovery bin: the Delete button and the expiry sweep file documents there, and only a document already in that folder is removed this way.

### Interrupted moves and the startup reconciler

Because the file moves before the commit, a crash leaves one of two states: the file is at the new path while the database still names the old one, or both are new. Nothing else is possible, so the repair rule is simple: **the database wins, and a stray file is moved to where the database says it belongs.**

`archive.Service.Reconcile` applies that rule once at startup, after the database opens and before the OCR workers, the expiry sweep and the watch folder start. It is safe to run again: a second run changes nothing. It does the following:

- Creates a directory for every folder in the database that has none.
- Finds each document's file by the ID at the start of its name. If the file is missing at the expected path and exists elsewhere in the tree, it moves the file and its sidecar there, then rewrites the sidecar.
- Reads `trash/` entries (`<id>-<nanos>-<filename>`). If the document row still exists, the file is restored to its expected path. If the row is gone, the delete is finished and the entry is removed.
- Removes empty directories that no folder row accounts for, such as the husk of an interrupted folder move.
- Reports, and leaves alone, anything it cannot fix: a file that is missing everywhere, two files that claim one ID, or a file that no row owns. These are logged at error level.

It does not run on a timer. A move that is in flight while it runs looks the same as an interrupted one, so it only runs when nothing else is changing the tree.

SQLite runs in WAL mode, so a power loss can drop the last committed transaction and leave the database older than the file. The same rule covers it.

## Intake and deduplication

- **Sniffing.** The file's bytes decide its type (PDF, PNG, JPEG, TIFF, WebP). The file name and the mail header do not. HEIC is recognised and rejected with a reason.
- **Identity.** Every document has a SHA-256. A second copy of the same bytes is reported as a duplicate and is not stored again, whatever its name or source.
- **Source references.** Each import also records a source reference: `name|size` for uploads and watch-folder files, and the Message-ID for mail. The pair of reference and hash is recorded in `ingest_log`. Seeing the same pair again returns the document it matched, so retries are safe.
- **Watch folder.** A file is imported only when its size and modification time are unchanged across scans and at least two seconds old, so a partly copied file is never read. Imported files move to `.imported/`, rejects to `.rejected/`, and a name clash gets " (n)".
- **Mail.** Unread messages (`UNSEEN`) are searched in the configured mailbox. Attachments with a real file name are imported. The message is marked read once its attachments are handled. Connections retry on transient errors with backoff.

- **Paperless.** An admin starts a run from the web app. It lists documents page by page, downloads each original, and hands it to the importer with its title, date, type and tags, and the folder chosen by the saved tag-to-folder rules (first matching rule, otherwise the run's default folder). A document that is already in the archive, from an earlier run or as the same bytes, is moved to the folder of its first matching rule through `archive.Service.Update` (so the file, sidecar and row move together) only while it is still in INBOX. A folder is assigned, never changed: a document that already has a folder, or matches no rule, stays where it is. The source reference is `host#id`, so a repeat run skips documents it filed before without downloading them. One run at a time, kept in memory, stopped on shutdown. A run stops at once when Paperless rejects the token, and after ten failures in a row.

## OCR

`ocr.Engine` finds `tesseract`, `pdftotext` and `pdftoppm` on the path at startup. The result is shown in **Admin → Text recognition**.

- A PDF with a text layer is read with `pdftotext`. Less than 20 characters counts as no text layer.
- Otherwise the first 40 pages are rendered at 300 dpi and read page by page, with the configured languages.
- If Tesseract is not installed, documents are marked `skipped`. Installing it later and pressing **Run OCR again** fixes them.
- Work is queued on import and re-queued by a scanner every 30 seconds, so a restart never loses pending documents.
- Each job has a time limit, set in the admin screen. Shutdown cancels running jobs, and they stay `pending` for the next start.

## Search

`documents_fts` (FTS5) indexes title, OCR text, tags, type name, with accent folding (`remove_diacritics 2`). Queries are split into words and matched as prefixes, ANDed together. Syntax characters in user input are quoted, so a stray `"` or `*` cannot break the query. Results rank by `bm25`, and snippets come from `snippet()` with control characters as highlight markers, which the client renders as text, never HTML.

## Web app

A React single-page app. The triage screen is the default. The library, document detail and admin screens are the other views. The app talks only to `/api/v1` with the HttpOnly session cookie, so no token is stored in the browser.

The folder picker on the triage card has a **New folder** button beside it: type a name, choose where it goes (top level or inside any folder), and the new folder is selected for the document. The same control sits beside the folder pickers on the Paperless import screen.

Keyboard shortcuts on triage: `J` and `K` move through the queue, `F` files the open document, `T` focuses the title, `S` focuses the folder. `Ctrl/⌘ K` opens search from any screen.

Visual tokens (Cyanotype) are in `frontend/src/app.css` and `frontend/src/theme.ts`.

## Security notes

- Passwords: argon2id. Sign-in failures return one generic message, and an unknown email is checked against a dummy hash so it takes as long to refuse as a wrong password. Each check holds 64 MiB, so at most two run at once, and a sign-in that waits more than 5 seconds for one gets `503 BUSY`.
- Sign-in attempts are counted before the password is checked, so parallel guesses cannot slip past a limit. Three limits apply: per client address, per email and address, and per email across browsers the account does not recognise (numbers in [api.md](api.md)). A successful sign-in sets a signed `archivr_device` cookie that names the account. That browser is then counted on its own and not against the account, so guessing from elsewhere cannot lock its owner out. Guessing can still block the account for 15 minutes on browsers it does not recognise.
- The client address is the remote address, or with `TRUST_PROXY` the `X-Forwarded-For` entry added by the outermost trusted proxy. Entries further left are set by the client and ignored. IPv6 addresses are counted per /64, since one host usually holds a whole /64.
- SSO roles are re-synced on each sign-in from the groups claim (see the README). The admin group must be set before SSO sign-ins.
- Sessions: HS256 JWT in an HttpOnly cookie, `SameSite=Lax`, `Secure` when served over HTTPS. The user is re-read from the database on each request, so a removed account or a demotion takes effect at once.
- Share links: a JWT signed with a key derived from the session secret, with its own audience (`archivr:share`), so it is never accepted as a session and a session is never accepted as a link. It carries the document number and the expiry the user chose (1 minute to 365 days), and opens only that document's metadata subset and file. Links are stateless, so one cannot be revoked alone: deleting the document, or changing `JWT_SECRET`, ends them. Without `JWT_SECRET`, links end when Archivr restarts.
- Cross-site requests: the API has no CSRF token. Instead, Go's `http.CrossOriginProtection` refuses every non-`GET` request that the browser marks as coming from another origin (`Sec-Fetch-Site`, or `Origin`), and JSON bodies must carry `Content-Type: application/json`, which a page on another site cannot send without a CORS preflight. The session cookie is also `SameSite=Lax`.
- Headers: every response has `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`, and `Strict-Transport-Security` when served over HTTPS. Web app pages also have a content security policy that allows only same-origin scripts, workers and connections, and `frame-ancestors 'none'`. Styles allow `'unsafe-inline'` because Mantine injects style elements.
- Files are served with `nosniff` and `Content-Disposition: inline` on their stored type. Names are validated for path traversal and reserved characters.
- Secrets (OIDC client secret, mail password) are never returned by the API. They appear masked as `********`, and a masked value sent back means "keep the stored one".
- Admin-only routes: types, settings, users. The last admin cannot be removed, and nobody can delete their own account.
