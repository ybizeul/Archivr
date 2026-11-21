-- Archivr initial schema.

-- Folders mirror the on-disk layout under DATA_DIR/documents.
-- The INBOX row is a system folder: it can't be renamed or deleted.
CREATE TABLE folders (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id   INTEGER REFERENCES folders(id) ON DELETE RESTRICT,
    name        TEXT    NOT NULL,
    path        TEXT    NOT NULL UNIQUE,
    is_system   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (name <> '' AND name NOT LIKE '%/%')
);

INSERT INTO folders (id, parent_id, name, path, is_system)
VALUES (1, NULL, 'INBOX', 'INBOX', 1);

CREATE TABLE document_types (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    color       TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE tags (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    name  TEXT NOT NULL UNIQUE COLLATE NOCASE
);

-- Document IDs are accession numbers. The first one issued is 10000
-- (see the sqlite_sequence seed below).
CREATE TABLE documents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    title           TEXT    NOT NULL,
    document_date   TEXT,                       -- YYYY-MM-DD, nullable until set
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    type_id         INTEGER REFERENCES document_types(id) ON DELETE RESTRICT,
    folder_id       INTEGER NOT NULL REFERENCES folders(id) ON DELETE RESTRICT,
    filename        TEXT    NOT NULL,           -- name on disk, relative to the folder
    mime            TEXT    NOT NULL,
    size            INTEGER NOT NULL CHECK (size >= 0),
    sha256          TEXT    NOT NULL,
    source          TEXT    NOT NULL CHECK (source IN ('watch', 'imap', 'upload')),
    source_ref      TEXT    NOT NULL DEFAULT '', -- Message-ID or watch-folder file name
    ocr_status      TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (ocr_status IN ('pending', 'done', 'failed', 'skipped')),
    ocr_error       TEXT    NOT NULL DEFAULT '',
    CHECK (document_date IS NULL OR document_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]')
);

CREATE INDEX documents_folder_idx ON documents(folder_id);
CREATE INDEX documents_type_idx   ON documents(type_id);
CREATE INDEX documents_sha256_idx ON documents(sha256);
CREATE INDEX documents_ocr_idx    ON documents(ocr_status);

CREATE TABLE document_tags (
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    tag_id      INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (document_id, tag_id)
);

CREATE INDEX document_tags_tag_idx ON document_tags(tag_id);

-- Extracted text, one row per document. Written by the OCR worker.
CREATE TABLE document_content (
    document_id INTEGER PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    body        TEXT    NOT NULL DEFAULT '',
    engine      TEXT    NOT NULL DEFAULT '',
    languages   TEXT    NOT NULL DEFAULT '',
    extracted_at TEXT   NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Full-text index over title, OCR text, tags and type.
-- Kept in sync by the store layer inside the same transaction as the write.
CREATE VIRTUAL TABLE documents_fts USING fts5(
    title,
    body,
    tags,
    type_name,
    document_id UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name          TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    password_hash TEXT NOT NULL DEFAULT '',   -- empty for OIDC-only accounts
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Key/value store for runtime configuration (auth, imap, ocr).
-- Values are JSON. Secrets are stored here and masked in API responses.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Records what has already been imported, so re-scans and polls are idempotent.
CREATE TABLE ingest_log (
    source      TEXT NOT NULL CHECK (source IN ('watch', 'imap', 'upload')),
    source_ref  TEXT NOT NULL,            -- Message-ID, or watch file name + size
    sha256      TEXT NOT NULL,
    document_id INTEGER REFERENCES documents(id) ON DELETE SET NULL,
    seen_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (source, source_ref, sha256)
);

-- Start document numbering at 10000: the next AUTOINCREMENT value is 10000.
INSERT INTO sqlite_sequence (name, seq) VALUES ('documents', 9999);
