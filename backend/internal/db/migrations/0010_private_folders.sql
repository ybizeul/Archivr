-- Private folders. A folder with a private_owner_id is shown and opened only for that user,
-- together with every folder and document beneath it. NULL means the folder is shared.
--
-- RESTRICT keeps a user's private data from turning public when the account is removed: the
-- account cannot be deleted while it owns private folders or documents.
ALTER TABLE folders ADD COLUMN private_owner_id INTEGER REFERENCES users(id) ON DELETE RESTRICT;

-- A document is private through its folder. The one exception is a document that leaves a
-- private folder for Trash, by hand or when it expires: Trash is shared, so the document keeps
-- its owner here and stays hidden from everyone else. Leaving Trash clears it.
ALTER TABLE documents ADD COLUMN private_owner_id INTEGER REFERENCES users(id) ON DELETE RESTRICT;

CREATE INDEX folders_private_owner_idx ON folders(private_owner_id) WHERE private_owner_id IS NOT NULL;
CREATE INDEX documents_private_owner_idx ON documents(private_owner_id) WHERE private_owner_id IS NOT NULL;
