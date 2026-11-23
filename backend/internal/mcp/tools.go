package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/store"
)

const (
	defaultPageSize = 25
	maxPageSize     = 100

	defaultTextChars = 20000
	maxTextChars     = 100000

	// maxFileBytes caps get_document_file. The file travels base64-encoded inside a JSON
	// message, so this keeps a reply to roughly 14 MB.
	maxFileBytes = 10 << 20

	maxCatalogItems = 200
)

var sortOrders = map[string]bool{"": true, "newest": true, "oldest": true, "date": true, "title": true}

func (t *tools) register(srv *mcp.Server) {
	readOnly := func(title string) *mcp.ToolAnnotations {
		no := false
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: &no}
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_folders",
		Description: "Browse the folder tree. Without parent_id it returns every folder, flat and sorted by path; each folder has its parent_id so the tree can be rebuilt. With parent_id it returns the direct subfolders of that folder, and parent_id 0 returns the top-level folders. INBOX holds unfiled documents and Trash holds deleted ones.",
		Annotations: readOnly("List folders"),
	}, t.listFolders)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_documents",
		Description: "List documents, a page at a time, with their metadata. Filter by folder, document type, tag and date range. Trash is left out unless it is the chosen folder. Use get_document for the text of a document.",
		Annotations: readOnly("List documents"),
	}, t.listDocuments)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_documents",
		Description: "Full-text search over titles, OCR text, tags, correspondents and types. Words are combined with AND and match as prefixes; accents and case are ignored. Results are ranked by relevance and carry a snippet with the matching words in **bold**. Takes the same filters as list_documents. Trash is left out unless it is the chosen folder.",
		Annotations: readOnly("Search documents"),
	}, t.searchDocuments)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_document",
		Description: "Read one document: its metadata (title, date, type, correspondent, tags, folder, file details, OCR status, expiry) and its extracted text. Long texts are paged: pass offset and max_chars, and continue from next_offset while truncated is true.",
		Annotations: readOnly("Get document"),
	}, t.getDocument)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_document_file",
		Description: "Return the original file of a document: an image for image documents, an embedded resource for PDFs. Files above 10 MB are refused; read the text with get_document instead.",
		Annotations: readOnly("Get document file"),
	}, t.getDocumentFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_catalog",
		Description: "List the document types, correspondents and tags in the archive with their document counts, most used first. Use it to find valid values for the type_id and tag filters. The optional query narrows correspondents and tags by name.",
		Annotations: readOnly("List types, correspondents and tags"),
	}, t.listCatalog)

	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "document",
		Title:       "Document",
		Description: "A document: its metadata as JSON, then its extracted text.",
		URITemplate: documentURIPrefix + "{id}",
	}, t.readResource)
}

// Documents -------------------------------------------------------------------------------

// Doc is a document as a client sees it.
type Doc struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	DocumentDate  *string  `json:"document_date" jsonschema:"the date on the document, YYYY-MM-DD, when known"`
	CreatedAt     string   `json:"created_at" jsonschema:"when the document was filed"`
	UpdatedAt     string   `json:"updated_at"`
	Type          string   `json:"type" jsonschema:"document type name, empty when none"`
	TypeID        *int64   `json:"type_id"`
	Correspondent string   `json:"correspondent" jsonschema:"sender or counterparty, empty when none"`
	FolderID      int64    `json:"folder_id"`
	FolderPath    string   `json:"folder_path"`
	Tags          []string `json:"tags"`
	Filename      string   `json:"filename"`
	Mime          string   `json:"mime"`
	Size          int64    `json:"size" jsonschema:"file size in bytes"`
	OCRStatus     string   `json:"ocr_status" jsonschema:"pending, done, failed or skipped"`
	ValidUntil    *string  `json:"valid_until" jsonschema:"last valid day, YYYY-MM-DD; the document moves to Trash after it"`
	Source        string   `json:"source" jsonschema:"how it arrived: watch, imap, upload or paperless"`
	Favorite      bool     `json:"favorite" jsonschema:"whether the user marked it as a favorite"`
	Snippet       string   `json:"snippet,omitempty" jsonschema:"in search results, text around the match with the matching words in **bold**"`
}

func toDoc(d store.Document) Doc {
	out := Doc{
		ID: d.ID, Title: d.Title, DocumentDate: d.DocumentDate, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		TypeID: d.TypeID, Correspondent: d.Correspondent, FolderID: d.FolderID, FolderPath: d.Folder.Path,
		Tags: nonNil(d.Tags), Filename: d.Filename, Mime: d.Mime, Size: d.Size, OCRStatus: d.OCRStatus,
		ValidUntil: d.ValidUntil, Source: d.Source, Favorite: d.Favorite, Snippet: markSnippet(d.Snippet),
	}
	if d.Type != nil {
		out.Type = d.Type.Name
	}
	return out
}

// markSnippet turns the highlight control characters of the store into Markdown bold.
func markSnippet(s string) string {
	s = strings.ReplaceAll(s, "\x01", "**")
	return strings.ReplaceAll(s, "\x02", "**")
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// Filters are shared by list_documents and search_documents.
type Filters struct {
	FolderID int64  `json:"folder_id,omitempty" jsonschema:"only documents in this folder; see list_folders"`
	Subtree  bool   `json:"subtree,omitempty" jsonschema:"with folder_id, include documents in its subfolders"`
	TypeID   int64  `json:"type_id,omitempty" jsonschema:"only documents of this type; see list_catalog"`
	Tag      string `json:"tag,omitempty" jsonschema:"only documents with this exact tag"`
	From     string `json:"from,omitempty" jsonschema:"only documents dated on or after this day, YYYY-MM-DD"`
	To       string `json:"to,omitempty" jsonschema:"only documents dated on or before this day, YYYY-MM-DD"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size, 1 to 100, default 25"`
	Offset   int    `json:"offset,omitempty" jsonschema:"number of results to skip"`
}

// ListInput is the input of list_documents.
type ListInput struct {
	Filters
	Sort string `json:"sort,omitempty" jsonschema:"newest (default, by date filed), oldest, date (by document date, newest first) or title"`
}

// SearchInput is the input of search_documents.
type SearchInput struct {
	Filters
	Query string `json:"query" jsonschema:"the words to search for"`
}

// DocList is a page of documents.
type DocList struct {
	Items  []Doc `json:"items"`
	Total  int   `json:"total" jsonschema:"how many documents match in all"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

func (t *tools) filter(ctx context.Context, f Filters) (store.DocFilter, error) {
	if f.From != "" && !archive.ValidDate(f.From) {
		return store.DocFilter{}, userError("from must be YYYY-MM-DD")
	}
	if f.To != "" && !archive.ValidDate(f.To) {
		return store.DocFilter{}, userError("to must be YYYY-MM-DD")
	}
	if f.Limit < 0 || f.Offset < 0 || f.FolderID < 0 || f.TypeID < 0 {
		return store.DocFilter{}, userError("limit, offset, folder_id and type_id cannot be negative")
	}
	limit := f.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	df := store.DocFilter{
		FolderID: f.FolderID, Subtree: f.Subtree, TypeID: f.TypeID, Tag: strings.TrimSpace(f.Tag),
		From: f.From, To: f.To, Limit: limit, Offset: f.Offset,
		ViewerID: t.user.ID, // never 0: that is the system viewer, which sees private folders
	}
	if f.FolderID != 0 {
		// A folder the user cannot see is reported as missing, as it is in the web app.
		if _, err := t.Store.FolderByIDFor(ctx, t.user.ID, f.FolderID); err != nil {
			return store.DocFilter{}, err
		}
		return df, nil
	}
	// The library and global search leave Trash out. Opening it is a choice of folder.
	trash, err := t.Store.FolderByPath(ctx, archive.TrashName)
	if err == nil {
		df.ExcludeFolderID = trash.ID
	} else if err != store.ErrNotFound {
		return store.DocFilter{}, err
	}
	return df, nil
}

func (t *tools) page(ctx context.Context, df store.DocFilter) (*mcp.CallToolResult, DocList, error) {
	docs, total, err := t.Store.ListDocuments(ctx, df)
	if err == nil {
		err = t.Store.FillFavorites(ctx, t.user.ID, docs)
	}
	if err != nil {
		return nil, DocList{}, t.fail(err, "folder")
	}
	out := DocList{Items: make([]Doc, len(docs)), Total: total, Limit: df.Limit, Offset: df.Offset}
	for i, d := range docs {
		out.Items[i] = toDoc(d)
	}
	return nil, out, nil
}

func (t *tools) listDocuments(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, DocList, error) {
	if !sortOrders[in.Sort] {
		return nil, DocList{}, userError("sort must be newest, oldest, date or title")
	}
	df, err := t.filter(ctx, in.Filters)
	if err != nil {
		return nil, DocList{}, t.fail(err, "folder")
	}
	df.Sort = in.Sort
	return t.page(ctx, df)
}

func (t *tools) searchDocuments(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, DocList, error) {
	query := strings.TrimSpace(in.Query)
	// A query with no words would match everything, which is a listing, not a search.
	if store.FTSQuery(query) == "" {
		return nil, DocList{}, userError("query needs at least one word or number to search for")
	}
	df, err := t.filter(ctx, in.Filters)
	if err != nil {
		return nil, DocList{}, t.fail(err, "folder")
	}
	df.Query = query
	df.Sort = "relevance"
	return t.page(ctx, df)
}

// Folders ---------------------------------------------------------------------------------

// FoldersInput is the input of list_folders.
type FoldersInput struct {
	ParentID *int64 `json:"parent_id,omitempty" jsonschema:"list the direct subfolders of this folder; 0 for the top level; leave out for all folders"`
}

// FoldersOutput is the result of list_folders.
type FoldersOutput struct {
	Folders []store.Folder `json:"folders"`
}

func (t *tools) listFolders(ctx context.Context, _ *mcp.CallToolRequest, in FoldersInput) (*mcp.CallToolResult, FoldersOutput, error) {
	all, err := t.Store.ListFoldersFor(ctx, t.user.ID)
	if err != nil {
		return nil, FoldersOutput{}, t.fail(err, "folder")
	}
	if in.ParentID == nil {
		return nil, FoldersOutput{Folders: nonNil(all)}, nil
	}
	parent := *in.ParentID
	if parent < 0 {
		return nil, FoldersOutput{}, userError("parent_id cannot be negative")
	}
	if parent != 0 {
		known := false
		for _, f := range all {
			if f.ID == parent {
				known = true
				break
			}
		}
		if !known {
			return nil, FoldersOutput{}, t.fail(store.ErrNotFound, "folder")
		}
	}
	var out []store.Folder
	for _, f := range all {
		if (f.ParentID == nil && parent == 0) || (f.ParentID != nil && *f.ParentID == parent) {
			out = append(out, f)
		}
	}
	return nil, FoldersOutput{Folders: nonNil(out)}, nil
}

// One document ----------------------------------------------------------------------------

// GetInput is the input of get_document.
type GetInput struct {
	ID       int64 `json:"id" jsonschema:"the document number"`
	Offset   int   `json:"offset,omitempty" jsonschema:"first character of the text to return, for paging; default 0"`
	MaxChars int   `json:"max_chars,omitempty" jsonschema:"most characters of text to return, up to 100000; default 20000"`
}

// DocText is a document with its extracted text, or a page of it.
type DocText struct {
	Doc
	Text       string `json:"text" jsonschema:"extracted text, empty until OCR is done"`
	TextLength int    `json:"text_length" jsonschema:"characters in the whole text"`
	TextOffset int    `json:"text_offset" jsonschema:"where this page of the text starts"`
	Truncated  bool   `json:"truncated" jsonschema:"true when more text follows this page"`
	NextOffset int    `json:"next_offset,omitempty" jsonschema:"offset to pass to read the next page"`
}

// document loads a document the user may see, with its favorite flag.
func (t *tools) document(ctx context.Context, id int64) (store.Document, error) {
	doc, err := t.Store.GetDocumentFor(ctx, t.user.ID, id)
	if err != nil {
		return store.Document{}, err
	}
	one := []store.Document{doc}
	if err := t.Store.FillFavorites(ctx, t.user.ID, one); err != nil {
		return store.Document{}, err
	}
	return one[0], nil
}

func (t *tools) getDocument(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, DocText, error) {
	if in.ID <= 0 {
		return nil, DocText{}, userError("id must be a positive number")
	}
	if in.Offset < 0 || in.MaxChars < 0 {
		return nil, DocText{}, userError("offset and max_chars cannot be negative")
	}
	doc, err := t.document(ctx, in.ID)
	if err != nil {
		return nil, DocText{}, t.fail(err, "document")
	}
	// The visibility check above comes first: DocumentText does not make one.
	text, err := t.Store.DocumentText(ctx, doc.ID)
	if err != nil {
		return nil, DocText{}, t.fail(err, "document")
	}
	maxChars := in.MaxChars
	if maxChars == 0 {
		maxChars = defaultTextChars
	}
	maxChars = min(maxChars, maxTextChars)
	page, total, truncated := slicePage(text, in.Offset, maxChars)
	out := DocText{Doc: toDoc(doc), Text: page, TextLength: total, TextOffset: min(in.Offset, total), Truncated: truncated}
	if truncated {
		out.NextOffset = out.TextOffset + utf8.RuneCountInString(page)
	}
	return nil, out, nil
}

// slicePage returns up to n characters of s from character offset, the number of characters
// in s, and whether any follow the page. Counting characters, not bytes, keeps a multi-byte
// letter whole.
func slicePage(s string, offset, n int) (page string, total int, truncated bool) {
	total = utf8.RuneCountInString(s)
	if offset >= total {
		return "", total, false
	}
	start := 0
	for i := 0; i < offset; i++ {
		_, size := utf8.DecodeRuneInString(s[start:])
		start += size
	}
	end := start
	for i := 0; i < n && end < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return s[start:end], total, end < len(s)
}

func (t *tools) getDocumentFile(ctx context.Context, _ *mcp.CallToolRequest, in struct {
	ID int64 `json:"id" jsonschema:"the document number"`
}) (*mcp.CallToolResult, any, error) {
	if in.ID <= 0 {
		return nil, nil, userError("id must be a positive number")
	}
	doc, err := t.Store.GetDocumentFor(ctx, t.user.ID, in.ID)
	if err != nil {
		return nil, nil, t.fail(err, "document")
	}
	if doc.Size > maxFileBytes {
		return nil, nil, userError(fmt.Sprintf("the file is %d bytes, above the %d byte limit; read the text with get_document", doc.Size, maxFileBytes))
	}
	path, err := t.Layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		return nil, nil, t.fail(err, "document")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Log.Error("document file missing", "id", doc.ID, "path", path, "error", err)
		return nil, nil, userError("the file is missing from the archive")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, nil, t.fail(err, "document")
	}
	if len(data) > maxFileBytes {
		return nil, nil, userError("the file is above the size limit; read the text with get_document")
	}
	var content mcp.Content
	if strings.HasPrefix(doc.Mime, "image/") {
		content = &mcp.ImageContent{Data: data, MIMEType: doc.Mime}
	} else {
		content = &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
			URI: fmt.Sprintf("%s%d/file", documentURIPrefix, doc.ID), MIMEType: doc.Mime, Blob: data,
		}}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("%s (%s, %d bytes)", doc.Filename, doc.Mime, len(data))},
		content,
	}}, nil, nil
}

// Catalog ---------------------------------------------------------------------------------

// CatalogInput is the input of list_catalog.
type CatalogInput struct {
	Query string `json:"query,omitempty" jsonschema:"only correspondents and tags whose name contains this text"`
}

// CatalogOutput is the result of list_catalog.
type CatalogOutput struct {
	Types          []store.DocType       `json:"types"`
	Correspondents []store.Correspondent `json:"correspondents"`
	Tags           []store.Tag           `json:"tags"`
}

func (t *tools) listCatalog(ctx context.Context, _ *mcp.CallToolRequest, in CatalogInput) (*mcp.CallToolResult, CatalogOutput, error) {
	q := strings.TrimSpace(in.Query)
	types, err := t.Store.ListTypesFor(ctx, t.user.ID)
	if err != nil {
		return nil, CatalogOutput{}, t.fail(err, "catalog")
	}
	corrs, err := t.Store.ListCorrespondentsFor(ctx, t.user.ID, q)
	if err != nil {
		return nil, CatalogOutput{}, t.fail(err, "catalog")
	}
	tags, err := t.Store.ListTagsFor(ctx, t.user.ID, q)
	if err != nil {
		return nil, CatalogOutput{}, t.fail(err, "catalog")
	}
	return nil, CatalogOutput{
		Types:          nonNil(types),
		Correspondents: nonNil(corrs[:min(len(corrs), maxCatalogItems)]),
		Tags:           nonNil(tags[:min(len(tags), maxCatalogItems)]),
	}, nil
}
