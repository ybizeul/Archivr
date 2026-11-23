// Package mcp serves the archive to Model Context Protocol clients over Streamable HTTP.
//
// The server is read-only: a client can browse folders, list and search documents, and read a
// document's metadata, text and file. Every tool acts for one signed-in user and sees what
// that user sees in the web app, private folders included and nothing more.
package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

const instructions = `Archivr is a private document archive. Documents are filed in folders and carry a title, ` +
	`a date, a type, a correspondent and tags. Scanned documents have OCR text.
Use list_folders to browse the folder tree, list_documents to list a folder, search_documents for full-text ` +
	`search, and get_document to read one document with its metadata and text. list_catalog shows the document ` +
	`types, correspondents and tags that can be used as filters. get_document_file returns the original file.
Everything is read-only.`

// Deps are what the tools read from.
type Deps struct {
	Store  *store.Store
	Layout storage.Layout
	Log    *slog.Logger
}

type ctxKey struct{}

// WithUser returns a context that carries the user the MCP request acts for. The caller
// authenticates the request first: this package trusts what it finds here.
func WithUser(ctx context.Context, u store.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

func userFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(store.User)
	return u, ok && u.ID != 0
}

// Handler serves MCP on one URL. The request context must carry a user, see WithUser.
func Handler(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	// A server is built per request so that its tools are bound to the caller. The cache keeps
	// the schema reflection from running every time.
	cache := mcp.NewSchemaCache()
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		user, ok := userFrom(r.Context())
		if !ok {
			return nil
		}
		return newServer(d, user, cache)
	}, &mcp.StreamableHTTPOptions{
		// No session state: each request is authenticated by its own token and stands alone, so
		// there is nothing to hijack and nothing to lose on a restart.
		Stateless:    true,
		JSONResponse: true,
		// The check refuses a loopback connection that names another host, which is how a
		// reverse proxy on the same machine looks. DNS rebinding needs ambient credentials,
		// and this endpoint takes only a bearer token that a web page cannot send.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        1 << 20,
	})
}

func newServer(d Deps, user store.User, cache *mcp.SchemaCache) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "archivr", Title: "Archivr", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: instructions, SchemaCache: cache})
	t := &tools{Deps: d, user: user}
	t.register(srv)
	return srv
}

type tools struct {
	Deps
	user store.User
}

// fail turns an error into the message a client sees. A missing or hidden item reads the same,
// so a client cannot tell a private document from one that does not exist. Anything unexpected
// is logged and reported without detail.
func (t *tools) fail(err error, what string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New(what + " not found")
	case errors.As(err, new(userError)):
		return err
	}
	t.Log.Error("mcp request failed", "user", t.user.ID, "error", err)
	return errors.New("something went wrong; the details are in the server log")
}

// userError is an error whose message is safe and useful to show to the client.
type userError string

func (e userError) Error() string { return string(e) }
