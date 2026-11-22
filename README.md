# Archivr

A self-hosted archive for scanned paper and PDFs. Documents arrive from a watch folder, email, or upload. Each one gets a number, is read with OCR, and is filed into folders you choose. Every file is also kept on disk in a plain folder tree, so the archive stays usable without the database.

- **Backend:** Go, SQLite (FTS5 search), no CGO.
- **Frontend:** React, Mantine, TanStack Query. The web app is embedded in the binary.
- **OCR:** Tesseract (English and French by default) and Poppler for PDFs.
- **Sign-in:** OpenID Connect (SSO), or local accounts.

See [docs/architecture.md](docs/architecture.md), [docs/data-model.md](docs/data-model.md) and [docs/api.md](docs/api.md).

## Quick start (development)

Requirements: Go 1.26, Node 24 or later, `tesseract` with `eng` and `fra` data, and `poppler` (`pdftoppm`, `pdftotext`). On macOS:

```sh
brew install tesseract tesseract-lang poppler
mise install            # Go and Node, per mise.toml
cp .env.example .env    # then set INIT_PASSWORD for the first sign-in
mise run dev            # backend on :8080, Vite on :5173
```

Open http://localhost:5173 and sign in with `INIT_ADMIN_EMAIL` and `INIT_PASSWORD`.

Without Tesseract the app still runs. Documents are marked OCR `skipped`, and **Admin → Text recognition** says what is missing. Without Poppler, PDFs get no text and no preview. Installing the tools and pressing **Run OCR again** fixes skipped documents.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | Address the server listens on. |
| `PUBLIC_URL` | derived from the request | External address, for the SSO callback and links. When unset, derived from the request, using `X-Forwarded-Host` and `X-Forwarded-Proto` if `TRUST_PROXY` is set. |
| `DATA_DIR` | `./data` | Holds `archivr.db`, `documents/`, `trash/`, `cache/`, and `ingest/` by default. |
| `WATCH_DIR` | `DATA_DIR/ingest` | Folder polled for new files. Imported files move to `.imported/`, rejects to `.rejected/`. |
| `OIDC_LOGOUT_URL` | from the provider's discovery document | Provider logout endpoint used when signing out with SSO. Set it when the provider does not advertise `end_session_endpoint`, or to use a different one. When set, sign-out skips discovery. |
| `TRUST_PROXY` | off | Set to any value behind a proxy you control. Reads `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto`. |
| `JWT_SECRET` | random per run | Signs session cookies and share links. Set it, or everyone is signed out and every share link stops working on restart. |
| `INIT_ADMIN_EMAIL` | `admin@archivr.local` | The first admin account. |
| `INIT_PASSWORD` | unset | Bootstrap password. **Applied on every start while set.** It resets that account's password and role, so remove it once you can sign in. |
| `DEBUG` | off | Verbose logging. |
| `DB_PATH` | `DATA_DIR/archivr.db` | Location of the SQLite file. Set by the container when Litestream is enabled. |
| `LITESTREAM_REPLICA_URL` | unset | Enables continuous replication with Litestream, for example `s3://bucket/archivr`. See below. |

Mail intake (IMAP) and sign-in (OIDC) are configured in **Admin** after first start, not in environment variables. OCR languages and time limits are configured there too.

### Replicating the database with Litestream

The image includes [Litestream](https://litestream.io). It is off unless `LITESTREAM_REPLICA_URL` is set. Without it, the server runs as before.

- With it, the database runs from the container's local disk at `/tmp/archivr.db` (`ARCHIVR_LOCAL_DB` changes this). It is not under `DATA_DIR`, so SQLite never writes its WAL to a network volume. Documents, the ingest folder, and the rest stay in `DATA_DIR`.
- On every start, the database is restored from the newest replica. Litestream then streams every change while Archivr runs. Litestream runs Archivr as its child process and forwards shutdown signals, so `docker stop` and pod termination stop both cleanly.
- If there is no replica yet but `DATA_DIR/archivr.db` exists, the container refuses to start. Starting would create an empty database beside the real documents. To enable replication on an existing archive, copy the database to the local path, then run `litestream replicate -once` with the same config so the first snapshot is written before the first start.
- Without `LITESTREAM_REPLICA_URL`, nothing changes: the database stays in `DATA_DIR`.
- For S3, set `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_REGION` when your provider needs it. For a local folder, use `file:///path/to/replica`.
- Replication covers the database only. Keep the `documents/` folder backed up as well. Files written after the last replicated change can exist without a database row. Their sidecars still hold the metadata.

### Importing from Paperless-ngx

In **Admin → Paperless**, enter the address of your Paperless-ngx server and an API token (Paperless → My Profile), press **Test connection**, then **Start import**. The import runs in the background and the screen shows progress.

- Each document keeps its title, date, document type, correspondent, and tags. Document types that do not exist yet are created. The Paperless correspondent goes into the document's correspondent field.
- Documents go to INBOX, or to a folder you pick.
- **File by tag.** Before starting, you can map Paperless tags to Archivr folders, so documents are filed directly instead of landing in INBOX. The rules are ordered: a document goes to the folder of the first rule whose tag it carries, and a document that matches no rule goes to the folder chosen above. Tags match without regard to case. Folders must already exist. Press **Get tag lists** to read the tags from Paperless: every tag appears with its document count and a folder picker, with a filter and an "only unmapped" switch, so you can map the tags you already have instead of typing names. Tags are read when you press the button, not every time the screen opens. Folders can be made on this screen without leaving it: **New folder** asks for a name and where it goes, at the top level or inside any folder, so you build a hierarchy one level at a time. The same button sits beside each folder picker and selects the folder it creates. The parent you used last is offered again, which makes several subfolders under one folder quick. Folders are created as soon as you press **Create folder**, and stay if you do not run the import. The rules are saved, and starting an import saves them first. The rules also apply to documents that are already in the archive, whether an earlier run imported them or the same file is there under another name: a document that matches a rule is moved to that folder, wherever it is filed now, including a folder you chose by hand. A document that matches no rule is never moved, and nothing is moved to the default folder after the fact. The result screen counts how many documents were moved. This makes the rules a way to sort out an earlier import: set them, then run the import again.
- The original file is imported. When Archivr cannot read the original (a Word file, for example) the PDF that Paperless made of it is used.
- Archivr reads every file again with its own text recognition. The text Paperless extracted is not copied over.
- Running it again only adds what is new. Documents already imported are skipped without being downloaded again, and a document whose bytes already exist in Archivr is reported as a duplicate.
- Nothing is changed in Paperless. The token only needs to read documents.
- A server with a self-signed or internal certificate needs **Ignore SSL certificate errors**. It is off by default, and with it on the certificate is not checked at all, so use it only on a network you trust. Adding your certificate authority to the system trust store is the safer fix.

### Single sign-on

In **Admin → Sign-in**, enter the issuer, client ID and secret, and register the callback URL shown on that screen with your identity provider.

- The role is set from the `groups` claim on **every** SSO sign-in. Members of the configured admin group get `admin`, everyone else gets `user`. Set the admin group before the first SSO sign-in, or an admin who signs in through SSO is demoted.
- **Disable local login** is only allowed while SSO is configured. Turn it on once SSO works, so you cannot lock yourself out.

## Where the files live

```
DATA_DIR/
  archivr.db                     index, metadata, search
  documents/
    INBOX/10000 - Landlord letter.pdf
    Home/Lease/10001 - Lease renewal.pdf
  cache/thumbs/                  preview images, named by content hash
  ingest/                        watch folder
```

Each document also has a small `….archivr.json` sidecar beside it with its number, title, date, type, tags and checksum. The sidecars keep the metadata next to the files, but no tool rebuilds the database from them yet.

Document numbers start at 10000 and are never reused.

## Docker

```sh
docker build -t archivr .
docker run -p 8080:8080 -v archivr-data:/data \
  -e PUBLIC_URL=http://localhost:8080 -e INIT_PASSWORD=change-me \
  archivr
```

The image includes Tesseract (English and French) and Poppler. `deploy/k8s/archivr.yaml` is a Kubernetes example with a Traefik route. Replace the placeholders before applying it.

## Testing

```sh
mise run test                      # backend, all packages
cd backend && go test -race ./...  # with the race detector
cd frontend && npm run lint && npm run build
mise run build-frontend            # copies the web build into the Go embed directory
```

`mise run build-frontend` keeps `backend/cmd/archivr/dist/.gitkeep`, so a fresh checkout still builds.

## Known limits

- **Deleting a document is permanent.** The file and its sidecar are removed once the delete is committed. There is no undo.
- HEIC photos are rejected with a message. Convert them to JPEG or PNG first.
- Email intake reads unread messages only. Mail is marked read after its attachments are filed.
- Files over 100 MB are refused.
- One server process per data directory. SQLite and the file tree are not shared between instances.
