package paperless

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPageSize = 100
	listTimeout     = 30 * time.Second
	fileTimeout     = 10 * time.Minute
	maxFileBytes    = 100 << 20 // same limit as uploads
)

// ErrAuth means Paperless refused the token. A run stops on it, since every
// following request would fail the same way.
var ErrAuth = errors.New("paperless rejected the API token")

// ErrTooLarge marks a file over the size Archivr accepts.
var ErrTooLarge = errors.New("file is larger than 100 MB")

// Client reads from one Paperless-ngx server.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// PageSize is how many documents one list request asks for. 0 means 100.
	PageSize int
}

func (c *Client) pageSize() int {
	if c.PageSize > 0 {
		return c.PageSize
	}
	return defaultPageSize
}

// NewClient builds a client from saved settings.
func NewClient(s Settings) *Client {
	hc := &http.Client{}
	if s.InsecureTLS {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // an explicit admin setting
		hc.Transport = t
	}
	return &Client{BaseURL: s.URL, Token: s.Token, HTTP: hc}
}

// Doc is the part of a Paperless document that Archivr keeps. The text is not
// requested: Archivr reads each file again with its own OCR settings.
type Doc struct {
	ID               int64   `json:"id"`
	Title            string  `json:"title"`
	Created          string  `json:"created"` // a date, or a date-time on older servers
	Correspondent    *int64  `json:"correspondent"`
	DocumentType     *int64  `json:"document_type"`
	Tags             []int64 `json:"tags"`
	OriginalFileName string  `json:"original_file_name"`
}

type page[T any] struct {
	Count   int     `json:"count"`
	Next    *string `json:"next"`
	Results []T     `json:"results"`
}

func (c *Client) do(ctx context.Context, path string, query url.Values, accept string) (*http.Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+c.Token)
	req.Header.Set("Accept", accept)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Paperless: %w", err)
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		res.Body.Close()
		return nil, ErrAuth
	case res.StatusCode == http.StatusNotFound:
		res.Body.Close()
		return nil, fmt.Errorf("%s was not found. Is this a Paperless-ngx address?", path)
	case res.StatusCode >= 300:
		defer res.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return nil, fmt.Errorf("paperless answered %s: %s", res.Status, strings.TrimSpace(string(snippet)))
	}
	return res, nil
}

func getPage[T any](ctx context.Context, c *Client, path string, q url.Values) (page[T], error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	res, err := c.do(ctx, path, q, "application/json")
	if err != nil {
		return page[T]{}, err
	}
	defer res.Body.Close()
	var p page[T]
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&p); err != nil {
		return page[T]{}, fmt.Errorf("unexpected answer from Paperless: %w", err)
	}
	return p, nil
}

// Count returns how many documents the token can see. It doubles as the connection test.
func (c *Client) Count(ctx context.Context) (int, error) {
	p, err := getPage[Doc](ctx, c, "/api/documents/", url.Values{"page_size": {"1"}, "fields": {"id"}})
	return p.Count, err
}

// Documents returns one page of documents, oldest ID first, with the total count
// and whether another page follows. Pages are numbered from 1.
//
// The end is read from "next" being null, which Paperless documents, and not from
// an empty page. The "next" address itself is ignored, because behind a proxy it
// can name a host this machine cannot reach.
func (c *Client) Documents(ctx context.Context, pageNo int) (docs []Doc, total int, more bool, err error) {
	p, err := getPage[Doc](ctx, c, "/api/documents/", url.Values{
		"page":      {strconv.Itoa(pageNo)},
		"page_size": {strconv.Itoa(c.pageSize())},
		"ordering":  {"id"},
		"fields":    {"id,title,created,correspondent,document_type,tags,original_file_name"},
	})
	if err != nil {
		// Documents removed during a run can leave the next page past the end. Paperless answers 404.
		if pageNo > 1 && strings.Contains(err.Error(), "was not found") {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	return p.Results, p.Count, p.Next != nil && len(p.Results) > 0, nil
}

type named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Names returns id to name for "tags", "correspondents" or "document_types".
func (c *Client) Names(ctx context.Context, kind string) (map[int64]string, error) {
	out := map[int64]string{}
	for n := 1; ; n++ {
		p, err := getPage[named](ctx, c, "/api/"+kind+"/", url.Values{
			"page":      {strconv.Itoa(n)},
			"page_size": {"500"},
			"fields":    {"id,name"},
		})
		if err != nil {
			return nil, err
		}
		for _, it := range p.Results {
			out[it.ID] = it.Name
		}
		if p.Next == nil || len(p.Results) == 0 {
			return out, nil
		}
	}
}

// TagInfo is a Paperless tag with the number of documents that carry it.
type TagInfo struct {
	Name          string `json:"name"`
	DocumentCount int    `json:"document_count"`
}

// Tags lists every tag, for building a tag-to-folder mapping before an import.
func (c *Client) Tags(ctx context.Context) ([]TagInfo, error) {
	type tag struct {
		Name          string `json:"name"`
		DocumentCount int    `json:"document_count"`
	}
	out := []TagInfo{}
	for n := 1; ; n++ {
		p, err := getPage[tag](ctx, c, "/api/tags/", url.Values{
			"page":      {strconv.Itoa(n)},
			"page_size": {"500"},
			"ordering":  {"name"},
			"fields":    {"id,name,document_count"},
		})
		if err != nil {
			return nil, err
		}
		for _, t := range p.Results {
			out = append(out, TagInfo{Name: t.Name, DocumentCount: t.DocumentCount})
		}
		if p.Next == nil || len(p.Results) == 0 {
			return out, nil
		}
	}
}

// Download returns a document's file. With original false it returns the version
// Paperless made itself, which is a PDF even for sources such as Word files.
func (c *Client) Download(ctx context.Context, id int64, original bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fileTimeout)
	defer cancel()
	res, err := c.do(ctx, fmt.Sprintf("/api/documents/%d/download/", id),
		url.Values{"original": {strconv.FormatBool(original)}}, "*/*")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.ContentLength > maxFileBytes {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download interrupted: %w", err)
	}
	if len(data) > maxFileBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}
