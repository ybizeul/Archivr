// Package store holds the SQL access code. Methods run on the pool, or inside a
// transaction when called through InTx, so a caller can combine several writes
// with a file operation and roll them all back together.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a unique constraint would be broken.
	ErrConflict = errors.New("already exists")
	// ErrInUse is returned when a row is still referenced and cannot be removed.
	ErrInUse = errors.New("in use")
)

// dbtx is satisfied by both *sql.DB and *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	DB *sql.DB
	q  dbtx
}

func New(db *sql.DB) *Store {
	return &Store{DB: db, q: db}
}

// InTx runs fn inside a transaction. fn receives a Store bound to that transaction.
// If fn returns an error, every write it made is rolled back.
// Nested calls join the outer transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if _, inTx := s.q.(*sql.Tx); inTx {
		return fn(s)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(&Store{DB: s.DB, q: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// mapErr turns SQLite constraint failures into the store's sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%w: %s", ErrInUse, msg)
	}
	return err
}

// GetJSON loads a settings row into v. It reports false when the key is absent.
func (s *Store) GetJSON(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read setting %s: %w", key, err)
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return false, fmt.Errorf("decode setting %s: %w", key, err)
	}
	return true, nil
}

// PutJSON stores v under key, replacing any previous value.
func (s *Store) PutJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	_, err = s.q.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(raw))
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}
