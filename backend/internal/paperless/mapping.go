package paperless

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/store"
)

const (
	mappingKey = "paperless_mapping"
	maxRules   = 200
)

// TagFolder files documents that carry a Paperless tag into a folder.
type TagFolder struct {
	Tag      string `json:"tag"` // Paperless tag name, matched without regard to case
	FolderID int64  `json:"folder_id"`
}

// Mapping is an ordered list of rules. A document goes to the folder of the first
// rule whose tag it carries, so the order is the priority. A document that matches
// no rule goes to the folder chosen for the run.
type Mapping struct {
	Rules []TagFolder `json:"rules"`
}

// LoadMapping returns the saved mapping, or an empty one.
func LoadMapping(ctx context.Context, st *store.Store) (Mapping, error) {
	var m Mapping
	if _, err := st.GetJSON(ctx, mappingKey, &m); err != nil {
		return Mapping{}, err
	}
	return m.Normalize(), nil
}

// SaveMapping stores m. Call Normalize and Validate first.
func SaveMapping(ctx context.Context, st *store.Store, m Mapping) error {
	return st.PutJSON(ctx, mappingKey, m)
}

// Normalize trims tag names and never leaves the rule list nil.
func (m Mapping) Normalize() Mapping {
	rules := make([]TagFolder, 0, len(m.Rules))
	for _, r := range m.Rules {
		rules = append(rules, TagFolder{Tag: strings.Join(strings.Fields(r.Tag), " "), FolderID: r.FolderID})
	}
	m.Rules = rules
	return m
}

// Validate checks the rules by themselves: a tag and a folder on each, no tag twice.
// Whether the folders exist is checked by CheckFolders.
func (m Mapping) Validate() error {
	if len(m.Rules) > maxRules {
		return fmt.Errorf("at most %d rules are allowed", maxRules)
	}
	seen := map[string]bool{}
	for i, r := range m.Rules {
		switch {
		case r.Tag == "":
			return fmt.Errorf("rule %d has no tag", i+1)
		case utf8.RuneCountInString(r.Tag) > 200:
			return fmt.Errorf("rule %d: the tag name is too long", i+1)
		case r.FolderID <= 0:
			return fmt.Errorf("rule %d (%s) has no folder", i+1, r.Tag)
		}
		key := strings.ToLower(r.Tag)
		if seen[key] {
			return fmt.Errorf("the tag %q appears in more than one rule", r.Tag)
		}
		seen[key] = true
	}
	return nil
}

// CheckFolders reports the first rule that names a folder that no longer exists.
func (m Mapping) CheckFolders(ctx context.Context, st *store.Store) error {
	for _, r := range m.Rules {
		if _, err := st.FolderByID(ctx, r.FolderID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: the folder for tag %q no longer exists", archive.ErrInvalid, r.Tag)
			}
			return err
		}
	}
	return nil
}

// FolderFor returns the folder for a document that carries these tag names.
func (m Mapping) FolderFor(tagNames []string) (int64, bool) {
	if len(m.Rules) == 0 || len(tagNames) == 0 {
		return 0, false
	}
	have := make(map[string]bool, len(tagNames))
	for _, t := range tagNames {
		have[strings.ToLower(strings.Join(strings.Fields(t), " "))] = true
	}
	for _, r := range m.Rules {
		if have[strings.ToLower(r.Tag)] {
			return r.FolderID, true
		}
	}
	return 0, false
}
