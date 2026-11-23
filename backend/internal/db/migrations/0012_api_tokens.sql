-- Personal access tokens. A user creates one to let a tool such as an MCP client read the
-- archive in their name. Only the SHA-256 of the token is kept, so a copy of the database
-- cannot be used to sign in. The rows go away with the user.
CREATE TABLE api_tokens (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL CHECK (name <> ''),
    token_hash    TEXT    NOT NULL UNIQUE,
    prefix        TEXT    NOT NULL,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_used_at  TEXT,
    expires_at    TEXT,
    revoked_at    TEXT
);

CREATE INDEX api_tokens_user_idx ON api_tokens(user_id);
