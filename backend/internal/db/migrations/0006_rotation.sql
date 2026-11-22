-- How the browser turns a document when it shows it, in degrees clockwise. This is display
-- metadata only: the file on disk is never changed.
ALTER TABLE documents ADD COLUMN rotation INTEGER NOT NULL DEFAULT 0
    CHECK (rotation IN (0, 90, 180, 270));
