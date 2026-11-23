package archive

import (
	"context"

	"github.com/archivr/backend/internal/store"
)

type actorKey struct{}

// WithActor marks a context as acting for a signed-in user. Mutations then act only on what
// that user can see: a document or folder hidden by a private folder is reported as not found,
// and a hidden folder is not a valid destination. A context without an actor is the system
// (importers, the expiry sweep, the startup reconciler) and is not restricted.
func WithActor(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

func actorOf(ctx context.Context) int64 {
	id, _ := ctx.Value(actorKey{}).(int64)
	return id
}

// requireDocument fails with ErrNotFound when the actor cannot see the document.
func requireDocument(ctx context.Context, tx *store.Store, id int64) error {
	actor := actorOf(ctx)
	if actor == 0 {
		return nil
	}
	ok, err := tx.DocumentVisibleTo(ctx, actor, id)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

// requireFolder fails with ErrNotFound when the actor cannot see the folder.
func requireFolder(ctx context.Context, tx *store.Store, id int64) error {
	actor := actorOf(ctx)
	if actor == 0 {
		return nil
	}
	ok, err := tx.FolderVisibleTo(ctx, actor, id)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}
