package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/archivr/backend/internal/store"
)

const documentURIPrefix = "archivr://documents/"

// readResource serves archivr://documents/{id}: the metadata as JSON, then the text.
func (t *tools) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	raw, ok := strings.CutPrefix(uri, documentURIPrefix)
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || id <= 0 {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	doc, err := t.document(ctx, id)
	if err != nil {
		// Hidden and missing documents look alike.
		if errors.Is(err, store.ErrNotFound) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, t.fail(err, "document")
	}
	text, err := t.Store.DocumentText(ctx, doc.ID)
	if err != nil {
		return nil, t.fail(err, "document")
	}
	meta, err := json.MarshalIndent(toDoc(doc), "", "  ")
	if err != nil {
		return nil, t.fail(err, "document")
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
		{URI: uri, MIMEType: "application/json", Text: string(meta)},
		{URI: uri, MIMEType: "text/plain", Text: text},
	}}, nil
}
