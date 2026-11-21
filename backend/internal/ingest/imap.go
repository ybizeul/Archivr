package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	// Registers the character set decoders that go-message needs for non-UTF-8 mail.
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"

	"github.com/archivr/backend/internal/store"
)

const (
	imapDialTimeout = 30 * time.Second
	imapRetries     = 3
	imapRetryWait   = 500 * time.Millisecond
	idlePollWait    = time.Minute
)

// IMAPPoller imports supported attachments from unread mail.
//
// Each message is identified by its Message-ID and each attachment by its hash, so a
// message seen twice is never filed twice. Messages are marked read once they are
// handled. A message that failed stays unread, so the next poll retries it.
type IMAPPoller struct {
	Importer *Importer
	Store    *store.Store
	Log      *slog.Logger
}

// PollSummary counts what one poll did.
type PollSummary struct {
	Messages    int
	Imported    int
	Duplicates  int
	Skipped     int // already imported before
	Unsupported int
	Failed      int
}

// Run polls on the interval from the settings until ctx is cancelled.
func (p *IMAPPoller) Run(ctx context.Context) {
	for {
		wait := idlePollWait
		s, err := LoadIMAPSettings(ctx, p.Store)
		switch {
		case err != nil:
			p.logger().Error("imap settings unreadable", "error", err)
		case s.Enabled:
			sum, err := p.PollOnce(ctx, s)
			if err != nil && ctx.Err() == nil {
				p.logger().Error("imap poll failed", "error", err)
			} else if sum.Messages > 0 {
				p.logger().Info("imap poll", "messages", sum.Messages, "imported", sum.Imported,
					"duplicates", sum.Duplicates, "unsupported", sum.Unsupported, "failed", sum.Failed)
			}
			wait = time.Duration(s.PollSeconds) * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (p *IMAPPoller) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// PollOnce runs one poll. Connection errors are retried a few times.
func (p *IMAPPoller) PollOnce(ctx context.Context, s IMAPSettings) (PollSummary, error) {
	var sum PollSummary
	err := withIMAPRetry(ctx, func() error {
		var err error
		sum, err = p.pollOnce(ctx, s)
		return err
	})
	return sum, err
}

func (p *IMAPPoller) pollOnce(ctx context.Context, s IMAPSettings) (PollSummary, error) {
	var sum PollSummary
	c, err := connect(ctx, s)
	if err != nil {
		return sum, err
	}
	defer c.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-stop:
		}
	}()

	if err := c.Login(s.User, s.Password).Wait(); err != nil {
		return sum, fmt.Errorf("imap login: %w", err)
	}
	if _, err := c.Select(s.Mailbox, nil).Wait(); err != nil {
		return sum, fmt.Errorf("imap select %s: %w", s.Mailbox, err)
	}

	searchData, err := c.UIDSearch(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil {
		return sum, fmt.Errorf("imap search: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return sum, nil
	}

	raws, err := fetchRaw(c, uids)
	if err != nil {
		return sum, err
	}

	var handled []imap.UID
	for _, m := range raws {
		sum.Messages++
		ok, err := p.processMessage(ctx, m.raw, &sum)
		if err != nil {
			sum.Failed++
			p.logger().Error("imap message failed, will retry", "uid", m.uid, "error", err)
			continue
		}
		if ok {
			handled = append(handled, m.uid)
		}
	}

	if len(handled) > 0 {
		cmd := c.Store(imap.UIDSetNum(handled...), &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagSeen},
		}, nil)
		if err := cmd.Close(); err != nil {
			p.logger().Error("could not mark messages read", "error", err)
		}
	}
	return sum, nil
}

type rawMessage struct {
	uid imap.UID
	raw []byte
}

// fetchRaw downloads the full message for each UID, without marking it read.
func fetchRaw(c *imapclient.Client, uids []imap.UID) ([]rawMessage, error) {
	cmd := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierNone, Peek: true},
		},
	})
	var out []rawMessage
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		var m rawMessage
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch data := item.(type) {
			case imapclient.FetchItemDataUID:
				m.uid = data.UID
			case imapclient.FetchItemDataBodySection:
				m.raw, _ = io.ReadAll(data.Literal)
			}
		}
		out = append(out, m)
	}
	if err := cmd.Close(); err != nil {
		return nil, fmt.Errorf("imap fetch: %w", err)
	}
	return out, nil
}

// processMessage files the supported attachments of one message.
// It reports whether the message is finished and can be marked read.
func (p *IMAPPoller) processMessage(ctx context.Context, raw []byte, sum *PollSummary) (bool, error) {
	atts, meta, err := ParseAttachments(raw)
	if err != nil {
		// A message that cannot be parsed will never parse. Mark it read so it stops coming back.
		p.logger().Warn("imap message unreadable, skipped", "error", err)
		sum.Unsupported++
		return true, nil
	}

	var supported []attachment
	for _, a := range atts {
		if mime, _ := SniffMIME(a.Data); mime != "" {
			supported = append(supported, a)
		} else {
			sum.Unsupported++
		}
	}
	if len(supported) == 0 {
		return true, nil
	}

	date := meta.Date.Local().Format("2006-01-02")
	for _, a := range supported {
		title := meta.Subject
		if title == "" {
			title = TitleFromFilename(a.Filename)
		} else if len(supported) > 1 {
			title = meta.Subject + " (" + TitleFromFilename(a.Filename) + ")"
		}
		res, err := p.Importer.Import(ctx, Candidate{
			Source:       "imap",
			SourceRef:    meta.MessageID,
			Filename:     a.Filename,
			Title:        title,
			DocumentDate: &date,
			Data:         a.Data,
		})
		if err != nil {
			return false, err
		}
		switch res.Status {
		case Imported:
			sum.Imported++
		case Duplicate:
			sum.Duplicates++
		case AlreadyImported:
			sum.Skipped++
		default:
			sum.Unsupported++
		}
	}
	return true, nil
}

// attachment is a file part of a message.
type attachment struct {
	Filename string
	Data     []byte
}

// messageMeta is the header information used for titles, dates and dedupe.
type messageMeta struct {
	MessageID string
	Subject   string
	Date      time.Time
}

// ParseAttachments returns the named file parts of a raw message, in order.
// Parts without a file name, such as the body text and inline signature images,
// are skipped. Nested multipart parts are walked by go-message.
func ParseAttachments(raw []byte) ([]attachment, messageMeta, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return nil, messageMeta{}, err
	}
	defer mr.Close()

	var meta messageMeta
	meta.MessageID, _ = mr.Header.MessageID()
	meta.Subject, _ = mr.Header.Subject()
	meta.Subject = strings.Join(strings.Fields(meta.Subject), " ")
	if meta.Subject == "" {
		meta.Subject = "Email attachment"
	}
	if d, err := mr.Header.Date(); err == nil {
		meta.Date = d
	} else {
		meta.Date = time.Now()
	}
	if meta.MessageID == "" {
		// Without a Message-ID, use the content. Identical mail still dedupes.
		sum := sha256.Sum256(raw)
		meta.MessageID = "sha256:" + hex.EncodeToString(sum[:])
	}

	var out []attachment
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if part == nil {
			if err != nil {
				return out, meta, err
			}
			continue
		}
		name := partFilename(part.Header)
		if name == "" {
			continue
		}
		data, rerr := io.ReadAll(part.Body)
		if rerr != nil {
			return out, meta, rerr
		}
		out = append(out, attachment{Filename: name, Data: data})
	}
	return out, meta, nil
}

// partFilename reads the file name from Content-Disposition, then from Content-Type.
func partFilename(h mail.PartHeader) string {
	dec := new(mime.WordDecoder)
	disp := h.Get("Content-Disposition")
	if _, params, err := mime.ParseMediaType(disp); err == nil {
		if n := params["filename"]; n != "" {
			return cleanName(dec, n)
		}
	}
	if _, params, err := mime.ParseMediaType(h.Get("Content-Type")); err == nil {
		if n := params["name"]; n != "" {
			return cleanName(dec, n)
		}
	}
	return ""
}

func cleanName(dec *mime.WordDecoder, name string) string {
	if decoded, err := dec.DecodeHeader(name); err == nil {
		name = decoded
	}
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// ListMailboxes returns the selectable mailboxes on the server.
func ListMailboxes(ctx context.Context, s IMAPSettings) ([]string, error) {
	var names []string
	err := withIMAPRetry(ctx, func() error {
		c, err := connect(ctx, s)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Login(s.User, s.Password).Wait(); err != nil {
			return fmt.Errorf("imap login: %w", err)
		}
		cmd := c.List("", "*", nil)
		names = nil
		for {
			mbox := cmd.Next()
			if mbox == nil {
				break
			}
			if !hasAttr(mbox.Attrs, imap.MailboxAttrNoSelect) {
				names = append(names, mbox.Mailbox)
			}
		}
		return cmd.Close()
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// TestIMAP connects, logs in and selects the mailbox, without changing anything.
func TestIMAP(ctx context.Context, s IMAPSettings) error {
	return withIMAPRetry(ctx, func() error {
		c, err := connect(ctx, s)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Login(s.User, s.Password).Wait(); err != nil {
			return fmt.Errorf("imap login: %w", err)
		}
		if _, err := c.Select(s.Mailbox, nil).Wait(); err != nil {
			return fmt.Errorf("imap select %s: %w", s.Mailbox, err)
		}
		return c.Logout().Wait()
	})
}

func hasAttr(attrs []imap.MailboxAttr, target imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == target {
			return true
		}
	}
	return false
}

// connect dials the server, with implicit TLS when s.TLS is set.
func connect(ctx context.Context, s IMAPSettings) (*imapclient.Client, error) {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	if s.TLS {
		dialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: imapDialTimeout},
			Config:    &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12},
		}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("imap connect: %w", err)
		}
		return imapclient.New(conn, nil), nil
	}
	conn, err := (&net.Dialer{Timeout: imapDialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap dial: %w", err)
	}
	return imapclient.New(conn, nil), nil
}

// withIMAPRetry retries transient network failures with exponential backoff.
// Authentication failures and cancellation are returned at once.
func withIMAPRetry(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= imapRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableIMAPError(err) || attempt == imapRetries || ctx.Err() != nil {
			return lastErr
		}
		timer := time.NewTimer(imapRetryWait * time.Duration(1<<(attempt-1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastErr
		case <-timer.C:
		}
	}
	return lastErr
}

func isRetryableIMAPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "authentication") || strings.Contains(msg, "invalid credentials") || strings.Contains(msg, "auth") {
		return false
	}
	for _, token := range []string{
		"timeout", "temporar", "connection reset", "broken pipe", "connection refused",
		"server closed", "unexpected eof", "tls handshake", "network is unreachable",
	} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}
