package store

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

// searchFoldVersion is the folding rules the index was built with.
// Bump it when Fold changes, so the next startup rebuilds the index.
const searchFoldVersion = 1

const searchFoldSetting = "search_fold"

// Letters that stay in one piece under Unicode normalization, mapped to the
// plain letters search should treat them as. Normalization leaves these
// unchanged, so the table is explicit. Keep it in step with frontend/src/lib/fold.ts.
var letterFold = map[rune]string{
	'æ': "ae", 'Æ': "ae",
	'œ': "oe", 'Œ': "oe",
	'ß': "ss", 'ẞ': "ss",
	'ø': "o", 'Ø': "o",
	'ł': "l", 'Ł': "l",
	'đ': "d", 'Đ': "d",
	'ð': "d", 'Ð': "d",
	'ħ': "h", 'Ħ': "h",
	'ŧ': "t", 'Ŧ': "t",
}

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("unaccent", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return "", nil
		}
		switch v := args[0].(type) {
		case string:
			return Fold(v), nil
		case []byte:
			return Fold(string(v)), nil
		default:
			return Fold(fmt.Sprint(v)), nil
		}
	})
}

// Fold reduces text so search can compare it. Accents are removed, ligatures
// expand (œ → oe, æ → ae, ß → ss), and a stroke through a letter is ignored
// (ø → o, ł → l). Case is folded too.
func Fold(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if repl, ok := letterFold[r]; ok {
			b.WriteString(repl)
			continue
		}
		b.WriteRune(r)
	}
	decomposed := norm.NFD.String(b.String())
	b.Reset()
	for _, r := range decomposed {
		if unicode.IsMark(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// foldedTerms splits free text the same way a search query does, then folds
// each word. At most 12 words are kept, matching FTSQuery.
func foldedTerms(q string) []string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > 12 {
		words = words[:12]
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		if f := Fold(w); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// foldSnippet builds a short excerpt of text with the same highlight markers
// as SQLite's snippet(). It is used when the match exists only in the folded
// index (a ligature or a stroke letter), which the original columns do not
// tokenise to the query.
func foldSnippet(text, query string) string {
	needles := foldedTerms(query)
	if text == "" || len(needles) == 0 {
		return ""
	}
	type word struct{ start, end int }
	var words []word
	start := -1
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words = append(words, word{start, i})
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, word{start, len(text)})
	}
	hit := -1
	matches := make([]bool, len(words))
	for i, w := range words {
		folded := Fold(text[w.start:w.end])
		for _, n := range needles {
			if strings.HasPrefix(folded, n) {
				matches[i] = true
				if hit < 0 {
					hit = i
				}
				break
			}
		}
	}
	if hit < 0 {
		return ""
	}
	from := hit - 6
	if from < 0 {
		from = 0
	}
	to := hit + 7
	if to > len(words) {
		to = len(words)
	}
	var b strings.Builder
	if from > 0 {
		b.WriteString("…")
	} else {
		b.WriteString(text[:words[0].start])
	}
	for i := from; i < to; i++ {
		if i > from {
			b.WriteString(text[words[i-1].end:words[i].start])
		}
		if matches[i] {
			b.WriteByte(1)
			b.WriteString(text[words[i].start:words[i].end])
			b.WriteByte(2)
		} else {
			b.WriteString(text[words[i].start:words[i].end])
		}
	}
	if to < len(words) {
		b.WriteString("…")
	} else {
		b.WriteString(text[words[len(words)-1].end:])
	}
	return b.String()
}
