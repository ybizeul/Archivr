-- The last day a document is valid, YYYY-MM-DD. Archivr moves a document into the Trash
-- system folder once this date has passed. NULL means the document does not expire.
ALTER TABLE documents ADD COLUMN valid_until TEXT
    CHECK (valid_until IS NULL OR valid_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]');

CREATE INDEX documents_valid_until_idx ON documents(valid_until) WHERE valid_until IS NOT NULL;

-- Where expired documents go. This is a folder like any other, so they stay searchable and
-- can be restored by moving them out. A top-level folder that is already called Trash
-- becomes the system folder instead of colliding with it.
INSERT OR IGNORE INTO folders (parent_id, name, path, is_system)
VALUES (NULL, 'Trash', 'Trash', 1);
UPDATE folders SET is_system = 1 WHERE parent_id IS NULL AND path = 'Trash' COLLATE NOCASE;
