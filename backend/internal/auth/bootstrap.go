package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/archivr/backend/internal/store"
)

// EnsureBootstrapAdmin creates or updates the admin account from INIT_PASSWORD.
// Like Helpdesk, the password is reset on every start while the variable is set,
// so an admin who loses the password can recover it by setting the variable.
// Leave the variable unset once the admin is working.
func EnsureBootstrapAdmin(ctx context.Context, st *store.Store, email, password string) error {
	if password == "" {
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	u, err := st.UserByEmail(ctx, email)
	switch {
	case err == nil:
		if err := st.UpdateUserAccess(ctx, u.ID, u.Name, "admin"); err != nil {
			return err
		}
		if err := st.SetPasswordHash(ctx, u.ID, hash); err != nil {
			return err
		}
		slog.Info("bootstrap admin password reset", "email", email)
	case err == store.ErrNotFound:
		if _, err := st.CreateUser(ctx, store.User{
			Email:        email,
			Name:         "Administrator",
			Role:         "admin",
			PasswordHash: hash,
		}); err != nil {
			return fmt.Errorf("create bootstrap admin: %w", err)
		}
		slog.Info("bootstrap admin created", "email", email)
	default:
		return err
	}
	return nil
}
