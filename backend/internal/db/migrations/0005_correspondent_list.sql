-- Correspondents become a value list, like document types. Each name is stored once,
-- and a document points at it. A name typed on a document that is not in the list
-- yet is added to the list when the document is saved.
CREATE TABLE correspondents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (name <> '')
);

-- Existing free-text names become the first entries of the list.
INSERT OR IGNORE INTO correspondents (name)
SELECT correspondent FROM documents WHERE correspondent <> '' ORDER BY id;

ALTER TABLE documents ADD COLUMN correspondent_id INTEGER REFERENCES correspondents(id) ON DELETE RESTRICT;

UPDATE documents
   SET correspondent_id = (
       SELECT c.id FROM correspondents c WHERE c.name = documents.correspondent COLLATE NOCASE)
 WHERE correspondent <> '';

CREATE INDEX documents_correspondent_idx ON documents(correspondent_id);

ALTER TABLE documents DROP COLUMN correspondent;
