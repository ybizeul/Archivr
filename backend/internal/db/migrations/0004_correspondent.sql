-- Who a document came from or was sent to. Free text, empty when unknown.
ALTER TABLE documents ADD COLUMN correspondent TEXT NOT NULL DEFAULT '';
