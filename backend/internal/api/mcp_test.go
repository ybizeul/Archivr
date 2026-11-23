package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/store"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpClient connects a real MCP client, over HTTP, as the owner of a new token.
func (f *fixture) mcpClient(c *http.Cookie) *mcp.ClientSession {
	f.t.Helper()
	srv := httptest.NewServer(f.h)
	f.t.Cleanup(srv.Close)
	tok := f.newToken(c, "mcp test", nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/api/v1/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{tok.Token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		f.t.Fatalf("connect: %v", err)
	}
	f.t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// toolOK calls a tool that must succeed and decodes its structured result.
func toolOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := callTool(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s failed: %s", name, resultText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: decode %s: %v", name, raw, err)
	}
}

// toolFails calls a tool that must report an error and returns the message.
func toolFails(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res := callTool(t, cs, name, args)
	if !res.IsError {
		t.Fatalf("%s succeeded, want an error: %v", name, res.StructuredContent)
	}
	return resultText(res)
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

type mcpDoc struct {
	ID         int64    `json:"id"`
	Title      string   `json:"title"`
	FolderID   int64    `json:"folder_id"`
	FolderPath string   `json:"folder_path"`
	Type       string   `json:"type"`
	Tags       []string `json:"tags"`
	Snippet    string   `json:"snippet"`
	Mime       string   `json:"mime"`
}

type mcpList struct {
	Items  []mcpDoc `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

func (l mcpList) ids() []int64 {
	out := []int64{}
	for _, d := range l.Items {
		out = append(out, d.ID)
	}
	return out
}

func (f *fixture) setText(id int64, text string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SaveOCRText(ctx, id, text, "tesseract", "eng"); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, id)
	}); err != nil {
		f.t.Fatal(err)
	}
}

func hasID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func TestMCPListsTools(t *testing.T) {
	f := newFixture(t)
	cs := f.mcpClient(readerCookie(t, f))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
	}
	for _, want := range []string{"list_folders", "list_documents", "search_documents", "get_document", "get_document_file", "list_catalog"} {
		if !got[want] {
			t.Errorf("missing tool %s; have %v", want, got)
		}
	}
	if len(res.Tools) != 6 {
		t.Errorf("%d tools, want 6 and all read-only", len(res.Tools))
	}
}

func TestMCPBrowseListAndGet(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	bills := f.newFolder(c, "Bills", 0)
	power := f.newFolder(c, "Power", bills.ID)
	f.newFolder(c, "Taxes", 0)

	electric := f.uploadInto(c, power.ID, "electric", map[string]any{
		"title": "Electricity invoice", "type": "Invoice", "tags": []string{"utilities"},
		"document_date": "2026-03-01", "correspondent": "EDF",
	})
	water := f.uploadInto(c, bills.ID, "water", map[string]any{
		"title": "Water invoice", "type": "Invoice", "document_date": "2025-06-01",
	})
	f.setText(electric, "Amount due: 88 euros for the cœur of the grid.")
	f.setText(water, "Water consumption 12 cubic metres.")

	cs := f.mcpClient(c)

	// Folders: the whole tree, the top level, and one folder's children.
	var tree struct {
		Folders []store.Folder `json:"folders"`
	}
	toolOK(t, cs, "list_folders", nil, &tree)
	paths := map[string]store.Folder{}
	for _, fo := range tree.Folders {
		paths[fo.Path] = fo
	}
	for _, p := range []string{"INBOX", "Trash", "Bills", "Bills/Power", "Taxes"} {
		if _, ok := paths[p]; !ok {
			t.Errorf("tree lacks %s: %v", p, paths)
		}
	}
	if paths["Bills"].Children != 1 || paths["Bills/Power"].Documents != 1 {
		t.Errorf("counts: %+v %+v", paths["Bills"], paths["Bills/Power"])
	}
	toolOK(t, cs, "list_folders", map[string]any{"parent_id": 0}, &tree)
	for _, fo := range tree.Folders {
		if fo.ParentID != nil {
			t.Errorf("top level includes %s", fo.Path)
		}
	}
	toolOK(t, cs, "list_folders", map[string]any{"parent_id": bills.ID}, &tree)
	if len(tree.Folders) != 1 || tree.Folders[0].Path != "Bills/Power" {
		t.Errorf("children of Bills = %+v", tree.Folders)
	}
	toolOK(t, cs, "list_folders", map[string]any{"parent_id": paths["Taxes"].ID}, &tree)
	if len(tree.Folders) != 0 {
		t.Errorf("a leaf folder has children: %+v", tree.Folders)
	}
	toolFails(t, cs, "list_folders", map[string]any{"parent_id": 99999})

	// Listing: by folder, with and without subfolders, by type, tag and date.
	var list mcpList
	toolOK(t, cs, "list_documents", nil, &list)
	if list.Total != 2 || list.Limit != 25 {
		t.Errorf("all documents: %+v", list)
	}
	toolOK(t, cs, "list_documents", map[string]any{"folder_id": bills.ID}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != water {
		t.Errorf("Bills only = %v", got)
	}
	toolOK(t, cs, "list_documents", map[string]any{"folder_id": bills.ID, "subtree": true}, &list)
	if list.Total != 2 {
		t.Errorf("Bills with subfolders = %d", list.Total)
	}
	toolOK(t, cs, "list_documents", map[string]any{"tag": "UTILITIES"}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != electric || list.Items[0].FolderPath != "Bills/Power" || list.Items[0].Type != "Invoice" {
		t.Errorf("by tag = %+v", list)
	}
	toolOK(t, cs, "list_documents", map[string]any{"from": "2026-01-01"}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != electric {
		t.Errorf("from 2026 = %v", got)
	}
	toolOK(t, cs, "list_documents", map[string]any{"limit": 1, "offset": 1, "sort": "oldest"}, &list)
	if list.Total != 2 || len(list.Items) != 1 || list.Offset != 1 {
		t.Errorf("paging = %+v", list)
	}
	toolOK(t, cs, "list_documents", map[string]any{"limit": 1000}, &list)
	if list.Limit != 100 {
		t.Errorf("limit not capped: %d", list.Limit)
	}
	toolFails(t, cs, "list_documents", map[string]any{"from": "yesterday"})
	toolFails(t, cs, "list_documents", map[string]any{"sort": "random"})
	toolFails(t, cs, "list_documents", map[string]any{"folder_id": 99999})

	// Search: ranked, folded, with a bold snippet.
	toolOK(t, cs, "search_documents", map[string]any{"query": "coeur"}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != electric {
		t.Fatalf("search coeur = %v", got)
	}
	if !strings.Contains(list.Items[0].Snippet, "**") || strings.ContainsAny(list.Items[0].Snippet, "\x01\x02") {
		t.Errorf("snippet = %q", list.Items[0].Snippet)
	}
	toolOK(t, cs, "search_documents", map[string]any{"query": "invoice", "folder_id": bills.ID, "subtree": true}, &list)
	if list.Total != 2 {
		t.Errorf("search invoice in Bills = %d", list.Total)
	}
	toolOK(t, cs, "search_documents", map[string]any{"query": "inv", "tag": "utilities"}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != electric {
		t.Errorf("prefix search with tag = %v", got)
	}
	toolFails(t, cs, "search_documents", map[string]any{"query": "  ?! "})
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_documents", Arguments: map[string]any{}}); err == nil && !res.IsError {
		t.Error("search without a query succeeded")
	}

	// One document.
	var doc struct {
		mcpDoc
		Text       string `json:"text"`
		TextLength int    `json:"text_length"`
		Truncated  bool   `json:"truncated"`
		NextOffset int    `json:"next_offset"`
		TextOffset int    `json:"text_offset"`
	}
	toolOK(t, cs, "get_document", map[string]any{"id": electric}, &doc)
	if doc.Title != "Electricity invoice" || doc.Text != "Amount due: 88 euros for the cœur of the grid." || doc.Truncated {
		t.Errorf("get_document = %+v", doc)
	}
	// Paging counts characters: "œ" is one, not two bytes.
	full := []rune(doc.Text)
	var page struct {
		Text       string `json:"text"`
		Truncated  bool   `json:"truncated"`
		NextOffset int    `json:"next_offset"`
		TextOffset int    `json:"text_offset"`
	}
	var got strings.Builder
	offset := 0
	for i := 0; i < 20; i++ {
		toolOK(t, cs, "get_document", map[string]any{"id": electric, "offset": offset, "max_chars": 10}, &page)
		got.WriteString(page.Text)
		if !page.Truncated {
			break
		}
		if page.NextOffset != offset+10 {
			t.Fatalf("next_offset = %d after offset %d", page.NextOffset, offset)
		}
		offset = page.NextOffset
	}
	if got.String() != string(full) || page.Truncated {
		t.Errorf("paged text = %q, want %q", got.String(), string(full))
	}
	toolOK(t, cs, "get_document", map[string]any{"id": electric, "offset": 5000}, &page)
	if page.Text != "" || page.Truncated {
		t.Errorf("past the end = %+v", page)
	}
	toolFails(t, cs, "get_document", map[string]any{"id": 424242})
	toolFails(t, cs, "get_document", map[string]any{"id": 0})

	// Catalog.
	var cat struct {
		Types []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Documents int    `json:"document_count"`
		} `json:"types"`
		Correspondents []struct {
			Name string `json:"name"`
		} `json:"correspondents"`
		Tags []struct {
			Name      string `json:"name"`
			Documents int    `json:"document_count"`
		} `json:"tags"`
	}
	toolOK(t, cs, "list_catalog", nil, &cat)
	if len(cat.Types) != 1 || cat.Types[0].Name != "Invoice" || cat.Types[0].Documents != 2 {
		t.Errorf("types = %+v", cat.Types)
	}
	if len(cat.Correspondents) != 1 || cat.Correspondents[0].Name != "EDF" {
		t.Errorf("correspondents = %+v", cat.Correspondents)
	}
	if len(cat.Tags) != 1 || cat.Tags[0].Name != "utilities" {
		t.Errorf("tags = %+v", cat.Tags)
	}
	toolOK(t, cs, "list_catalog", map[string]any{"query": "zzz"}, &cat)
	if len(cat.Tags) != 0 || len(cat.Correspondents) != 0 {
		t.Errorf("query did not narrow the catalog: %+v", cat)
	}

	// The same document as a resource.
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: fmt.Sprintf("archivr://documents/%d", electric)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 2 || !strings.Contains(res.Contents[0].Text, "Electricity invoice") || !strings.Contains(res.Contents[1].Text, "88 euros") {
		t.Errorf("resource = %+v", res.Contents)
	}
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "archivr://documents/424242"}); err == nil {
		t.Error("reading a missing document resource succeeded")
	}
}

func TestMCPGetDocumentFile(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	id := f.uploadInto(c, archive.InboxID, "scan", nil)
	cs := f.mcpClient(c)

	res := callTool(t, cs, "get_document_file", map[string]any{"id": id})
	if res.IsError {
		t.Fatalf("get_document_file: %s", resultText(res))
	}
	var found bool
	for _, content := range res.Content {
		if er, ok := content.(*mcp.EmbeddedResource); ok {
			found = true
			if er.Resource.MIMEType != "application/pdf" || string(er.Resource.Blob) != string(pdfData("scan")) {
				t.Errorf("resource = %s %q", er.Resource.MIMEType, er.Resource.Blob)
			}
		}
	}
	if !found {
		t.Errorf("no embedded resource in %+v", res.Content)
	}
	toolFails(t, cs, "get_document_file", map[string]any{"id": 424242})
}

func TestMCPLeavesTrashOutUnlessChosen(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	trash, err := f.store.FolderByPath(context.Background(), archive.TrashName)
	if err != nil {
		t.Fatal(err)
	}
	kept := f.uploadInto(c, archive.InboxID, "kept", map[string]any{"title": "Kept report"})
	binned := f.uploadInto(c, trash.ID, "binned", map[string]any{"title": "Binned report"})
	cs := f.mcpClient(c)

	var list mcpList
	toolOK(t, cs, "list_documents", nil, &list)
	if got := list.ids(); !hasID(got, kept) || hasID(got, binned) {
		t.Errorf("list = %v", got)
	}
	toolOK(t, cs, "search_documents", map[string]any{"query": "report"}, &list)
	if got := list.ids(); !hasID(got, kept) || hasID(got, binned) {
		t.Errorf("search = %v", got)
	}
	toolOK(t, cs, "list_documents", map[string]any{"folder_id": trash.ID}, &list)
	if got := list.ids(); len(got) != 1 || got[0] != binned {
		t.Errorf("Trash = %v", got)
	}
}

// A private folder is the owner's alone. The MCP server must keep to that for every tool,
// and an admin gets no way around it.
func TestMCPKeepsPrivateFoldersPrivate(t *testing.T) {
	f := newFixture(t)
	owner, admin := readerCookie(t, f), adminCookie(t, f)

	secret := f.newFolder(owner, "Secret", 0)
	inner := f.newFolder(owner, "Inner", secret.ID)
	shared := f.newFolder(owner, "Shared", 0)
	hidden := f.uploadInto(owner, inner.ID, "hidden", map[string]any{"title": "Hidden diary", "tags": []string{"diary"}, "correspondent": "Dear Diary"})
	open := f.uploadInto(owner, shared.ID, "open", map[string]any{"title": "Open diary"})
	f.setText(hidden, "my secret passphrase")
	f.setText(open, "nothing to see")
	if rec := f.setPrivate(owner, secret.ID, true); rec.Code != http.StatusOK {
		t.Fatalf("make private: %d %s", rec.Code, rec.Body.String())
	}

	check := func(who string, cs *mcp.ClientSession, sees bool) {
		t.Helper()
		var tree struct {
			Folders []store.Folder `json:"folders"`
		}
		toolOK(t, cs, "list_folders", nil, &tree)
		for _, fo := range tree.Folders {
			if strings.HasPrefix(fo.Path, "Secret") && !sees {
				t.Errorf("%s sees folder %s", who, fo.Path)
			}
		}
		toolOK(t, cs, "list_folders", map[string]any{"parent_id": 0}, &tree)
		for _, fo := range tree.Folders {
			if fo.ID == secret.ID && !sees {
				t.Errorf("%s sees the private folder at the top level", who)
			}
		}

		var list mcpList
		toolOK(t, cs, "list_documents", nil, &list)
		if hasID(list.ids(), hidden) != sees {
			t.Errorf("%s: list_documents shows hidden = %v, want %v", who, hasID(list.ids(), hidden), sees)
		}
		toolOK(t, cs, "search_documents", map[string]any{"query": "diary"}, &list)
		if hasID(list.ids(), hidden) != sees || !hasID(list.ids(), open) {
			t.Errorf("%s: search = %v", who, list.ids())
		}
		toolOK(t, cs, "search_documents", map[string]any{"query": "passphrase"}, &list)
		if (list.Total == 1) != sees {
			t.Errorf("%s: search in private text = %d", who, list.Total)
		}

		var cat struct {
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
			Correspondents []struct {
				Name string `json:"name"`
			} `json:"correspondents"`
		}
		toolOK(t, cs, "list_catalog", nil, &cat)
		if (len(cat.Tags) == 1) != sees || (len(cat.Correspondents) == 1) != sees {
			t.Errorf("%s: catalog shows private names: %+v", who, cat)
		}

		if sees {
			var doc struct {
				Text string `json:"text"`
			}
			toolOK(t, cs, "get_document", map[string]any{"id": hidden}, &doc)
			if doc.Text != "my secret passphrase" {
				t.Errorf("%s: text = %q", who, doc.Text)
			}
			return
		}
		// Hidden looks the same as missing.
		missing := toolFails(t, cs, "get_document", map[string]any{"id": 424242})
		if msg := toolFails(t, cs, "get_document", map[string]any{"id": hidden}); msg != missing {
			t.Errorf("%s: hidden document says %q, missing says %q", who, msg, missing)
		}
		toolFails(t, cs, "get_document_file", map[string]any{"id": hidden})
		toolFails(t, cs, "list_documents", map[string]any{"folder_id": secret.ID})
		toolFails(t, cs, "list_documents", map[string]any{"folder_id": inner.ID})
		toolFails(t, cs, "list_folders", map[string]any{"parent_id": secret.ID})
		if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: fmt.Sprintf("archivr://documents/%d", hidden)}); err == nil {
			t.Errorf("%s: read a private document as a resource", who)
		}
	}

	check("owner", f.mcpClient(owner), true)
	check("admin", f.mcpClient(admin), false)
}
