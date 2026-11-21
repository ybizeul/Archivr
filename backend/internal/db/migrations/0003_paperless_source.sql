-- Documents imported from Paperless-ngx carry source 'paperless'.
--
-- SQLite cannot alter a CHECK constraint. Rebuilding `documents` is not an option:
-- dropping it with foreign keys on would cascade-delete every tag link and every
-- extracted text. SQLite documents rewriting the constraint text in the schema as
-- the safe route when the on-disk format does not change, and that is the case
-- here: the new list only adds a value. `writable_schema=RESET` reloads the schema
-- so the new constraint applies to this connection at once.
PRAGMA writable_schema = ON;

UPDATE sqlite_schema
   SET sql = replace(sql,
         'CHECK (source IN (''watch'', ''imap'', ''upload''))',
         'CHECK (source IN (''watch'', ''imap'', ''upload'', ''paperless''))')
 WHERE type = 'table' AND name IN ('documents', 'ingest_log');

PRAGMA writable_schema = RESET;
