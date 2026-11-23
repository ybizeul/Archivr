-- Favourite folders. Each user keeps their own list: one row per user and folder.
-- The rows go away with the user or the folder.
CREATE TABLE folder_favorites (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id   INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, folder_id)
);

CREATE INDEX folder_favorites_folder_idx ON folder_favorites(folder_id);
