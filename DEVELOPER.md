# Developer guide

This file is for people who build, change or test Archivr. If you only want to run it, see the [README](README.md).

## Stack

- **Backend:** Go, SQLite (FTS5 search), no CGO.
- **Frontend:** React, Mantine, TanStack Query. The web app is embedded in the Go binary.
- **OCR:** Tesseract (English and French by default) and Poppler for PDFs.
- **Sign-in:** OpenID Connect (SSO), or local accounts.

## Documentation

- [docs/architecture.md](docs/architecture.md): packages, mutation rules, intake, OCR, search, security notes.
- [docs/data-model.md](docs/data-model.md): tables and what they hold.
- [docs/api.md](docs/api.md): every route under `/api/v1`.

## Repository layout

| Path | Contents |
| --- | --- |
| `backend/` | The Go module. `cmd/archivr` is the entry point, `internal/` holds the packages. |
| `frontend/` | The React app (Vite). |
| `docs/` | Architecture, data model and API notes, and the README screenshots. |
| `deploy/` | Deployment examples, including Kubernetes. |
| `Dockerfile` | The container image: Archivr, Tesseract, Poppler and Litestream. |
| `mise.toml` | Tool versions and the development tasks. |

## Running in development

Requirements: Go 1.26, Node 24 or later, `tesseract` with `eng` and `fra` data, and `poppler` (`pdftoppm`, `pdftotext`). On macOS:

```sh
brew install tesseract tesseract-lang poppler
mise install            # Go and Node, per mise.toml
cp .env.example .env    # then set INIT_PASSWORD for the first sign-in
mise run dev            # backend on :8080, Vite on :5173
```

Open http://localhost:5173 and sign in with `INIT_ADMIN_EMAIL` and `INIT_PASSWORD`.

Without Tesseract the app still runs. Documents are marked OCR `skipped`, and **Admin → Text recognition** says what is missing. Without Poppler, PDFs get no text and no preview. Installing the tools and pressing **Run OCR again** fixes skipped documents.

Useful tasks (see `mise.toml`):

| Task | What it does |
| --- | --- |
| `mise run dev` | Backend and Vite dev server together. |
| `mise run back` | Backend only, with `.env` loaded. |
| `mise run front` | Vite dev server only. |
| `mise run test` | Backend tests, all packages. |
| `mise run build-frontend` | Builds the web app and copies it into the Go embed directory. |

`mise run build-frontend` keeps `backend/cmd/archivr/dist/.gitkeep`, so a fresh checkout still builds. Run it before `go build` when you want a binary that serves the current web app.

## Testing

```sh
mise run test                      # backend, all packages
cd backend && go test -race ./...  # with the race detector
cd frontend && npm run lint && npm run build
```

## Building the image

```sh
docker build -t archivr .
```

## Configuration reference

The environment variables are listed in the [README](README.md#configuration) and in `.env.example`. Mail intake, sign-in, OCR languages and the Paperless import are stored in the database and set from the **Admin** screens, not from the environment.

## Screenshots

The images in `docs/screenshots/` come from a throwaway instance filled with made-up documents. Do not use screenshots of a real archive.
