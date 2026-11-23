package api

import (
	"net/http"
	"strings"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/auth"
	mcpserver "github.com/archivr/backend/internal/mcp"
)

// mcpAuth lets a request through to the MCP handler only with a personal access token in an
// Authorization: Bearer header. The session cookie does not count: a browser sends it on its
// own, and the MCP endpoint is for tools that hold a token on purpose. The token's owner is
// loaded fresh, so a deleted account or a revoked token stops working at once.
func (s *server) mcpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !auth.IsToken(token) {
			s.mcpUnauthorized(w)
			return
		}
		user, err := s.Store.UserByToken(r.Context(), auth.HashToken(token), s.Now())
		if err != nil {
			s.mcpUnauthorized(w)
			return
		}
		ctx := archive.WithActor(mcpserver.WithUser(r.Context(), user), user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) mcpUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="archivr"`)
	writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "send a personal access token as Authorization: Bearer arc_...")
}
