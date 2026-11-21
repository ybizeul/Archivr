-- Documents, folders and dedupe.

-- A document's bytes are identified by their SHA-256. The same bytes must not be
-- filed twice, even when two imports race, so the index is unique.
DROP INDEX documents_sha256_idx;
CREATE UNIQUE INDEX documents_sha256_unique ON documents(sha256);

-- macOS and Windows filesystems are case-insensitive, so "Finance" and "finance"
-- must not both exist as folders.
CREATE UNIQUE INDEX folders_path_nocase ON folders(path COLLATE NOCASE);

CREATE INDEX documents_date_idx ON documents(document_date);
CREATE INDEX documents_created_idx ON documents(created_at);
