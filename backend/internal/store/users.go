package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Role         string `json:"role"` // "admin" or "user"
	PasswordHash string `json:"-"`
	// HasPassword tells the admin screen whether local sign-in is set up.
	HasPassword bool   `json:"has_password"`
	CreatedAt   string `json:"created_at"`
}

const userColumns = `id, email, name, role, password_hash, created_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	u.HasPassword = u.PasswordHash != ""
	return u, err
}

// UserByEmail matches case-insensitively, as the column is declared NOCASE.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CountAdmins is used to keep at least one admin in place.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY email COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser inserts a user and returns it with its new ID.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	res, err := s.q.ExecContext(ctx,
		`INSERT INTO users (email, name, role, password_hash) VALUES (?, ?, ?, ?)`,
		u.Email, u.Name, u.Role, u.PasswordHash)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", mapErr(err))
	}
	u.ID, err = res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// UpdateUserAccess sets name and role. Used on each OIDC sign-in, so the
// role follows the identity provider's groups.
func (s *Store) UpdateUserAccess(ctx context.Context, id int64, name, role string) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE users SET name = ?, role = ? WHERE id = ?`, name, role, id)
	if err != nil {
		return fmt.Errorf("update user access: %w", err)
	}
	return nil
}

func (s *Store) SetPasswordHash(ctx context.Context, id int64, hash string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
