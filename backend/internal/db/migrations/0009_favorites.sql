-- Favourite documents. Each user keeps their own list: one row per user and document.
-- The rows go away with the user or the document.
CREATE TABLE document_favorites (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, document_id)
);

CREATE INDEX document_favorites_document_idx ON document_favorites(document_id);
