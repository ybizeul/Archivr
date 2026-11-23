package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// timeLayout matches the strftime format the schema uses for its timestamps, so the stored
// values compare correctly as text.
const timeLayout = "2006-01-02T15:04:05.000Z"

// lastUsedGranularity keeps a busy client from writing to the database on every request.
const lastUsedGranularity = time.Minute

// APIToken is a personal access token as shown to its owner. The secret itself is never kept.
type APIToken struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
}

// CreateToken stores the hash of a new token for a user. expires is nil for a token that
// does not expire.
func (s *Store) CreateToken(ctx context.Context, userID int64, name, hash, prefix string, now time.Time, expires *time.Time) (APIToken, error) {
	created := now.UTC().Format(timeLayout)
	var exp *string
	if expires != nil {
		v := expires.UTC().Format(timeLayout)
		exp = &v
	}
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, name, hash, prefix, created, exp)
	if err != nil {
		return APIToken{}, fmt.Errorf("create token: %w", mapErr(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return APIToken{}, err
	}
	return APIToken{ID: id, Name: name, Prefix: prefix, CreatedAt: created, ExpiresAt: exp}, nil
}

// ListTokens returns a user's tokens that have not been revoked, newest first.
func (s *Store) ListTokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT id, name, prefix, created_at, last_used_at, expires_at
		  FROM api_tokens
		 WHERE user_id = ? AND revoked_at IS NULL
		 ORDER BY id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken ends one of a user's tokens. It returns ErrNotFound when the user has no such
// live token, so one user cannot probe another's token IDs.
func (s *Store) RevokeToken(ctx context.Context, userID, id int64, now time.Time) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		now.UTC().Format(timeLayout), id, userID)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserByToken finds the owner of a token by its hash. A revoked or expired token gives
// ErrNotFound. The user row is read fresh, so a deleted account or a changed role takes
// effect at once. It notes the use of the token at most once a minute.
func (s *Store) UserByToken(ctx context.Context, hash string, now time.Time) (User, error) {
	var (
		tokenID int64
		expires sql.NullString
		used    sql.NullString
		userID  int64
	)
	err := s.q.QueryRowContext(ctx, `
		SELECT id, user_id, expires_at, last_used_at
		  FROM api_tokens
		 WHERE token_hash = ? AND revoked_at IS NULL`, hash).Scan(&tokenID, &userID, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find token: %w", err)
	}
	stamp := now.UTC().Format(timeLayout)
	if expires.Valid && expires.String <= stamp {
		return User{}, ErrNotFound
	}
	user, err := s.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	cutoff := now.Add(-lastUsedGranularity).UTC().Format(timeLayout)
	if !used.Valid || used.String < cutoff {
		if _, err := s.q.ExecContext(ctx,
			`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, stamp, tokenID); err != nil {
			return User{}, fmt.Errorf("note token use: %w", err)
		}
	}
	return user, nil
}
