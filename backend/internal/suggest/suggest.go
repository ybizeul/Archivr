// Package suggest proposes a date and titles for a document from its OCR text.
// Suggestions are offered to the user, never applied automatically.
package suggest

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Date is a candidate document date with a short label for the UI.
type Date struct {
	Value  string `json:"value"`  // YYYY-MM-DD
	Label  string `json:"label"`  // "14 Oct 2026"
	Source string `json:"source"` // always "text" for now
}

// Result is what the triage screen shows as suggestion chips.
type Result struct {
	Dates  []Date   `json:"dates"`
	Titles []string `json:"titles"`
}

const (
	maxDates  = 5
	maxTitles = 3
	maxTitleR = 90
)

// dateHit is a date found at byte offset pos, with the code that records it.
type dateHit struct {
	pos   int
	apply func()
}

var months = map[string]int{
	"jan": 1, "janv": 1, "january": 1, "janvier": 1,
	"feb": 2, "fev": 2, "fevr": 2, "february": 2, "fevrier": 2,
	"mar": 3, "mars": 3, "march": 3,
	"apr": 4, "avr": 4, "april": 4, "avril": 4,
	"may": 5, "mai": 5,
	"jun": 6, "juin": 6, "june": 6,
	"jul": 7, "juil": 7, "july": 7, "juillet": 7,
	"aug": 8, "aout": 8, "august": 8,
	"sep": 9, "sept": 9, "september": 9, "septembre": 9,
	"oct": 10, "october": 10, "octobre": 10,
	"nov": 11, "november": 11, "novembre": 11,
	"dec": 12, "decembre": 12, "december": 12,
}

var (
	isoRe  = regexp.MustCompile(`\b((?:19|20)\d{2})[-/.](\d{1,2})[-/.](\d{1,2})\b`)
	numRe  = regexp.MustCompile(`\b(\d{1,2})[./-](\d{1,2})[./-]((?:19|20)\d{2}|\d{2})\b`)
	textRe = regexp.MustCompile(`(?i)\b(\d{1,2})(?:er|st|nd|rd|th)?\s+([\p{L}]{3,9})\.?,?\s+((?:19|20)\d{2})\b`)
)

// Analyse looks for dates and title candidates in OCR text.
func Analyse(text string) Result {
	res := Result{Dates: []Date{}, Titles: []string{}}
	seen := map[string]bool{}
	add := func(y, m, d int) {
		if len(res.Dates) >= maxDates {
			return
		}
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if t.Year() != y || int(t.Month()) != m || t.Day() != d {
			return // rejects 31 February and friends
		}
		value := t.Format("2006-01-02")
		if seen[value] {
			return
		}
		seen[value] = true
		res.Dates = append(res.Dates, Date{Value: value, Label: t.Format("2 Jan 2006"), Source: "text"})
	}

	var hits []dateHit
	for _, m := range isoRe.FindAllStringSubmatchIndex(text, -1) {
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		d, _ := strconv.Atoi(text[m[6]:m[7]])
		hits = append(hits, dateHit{m[0], func() { add(y, mo, d) }})
	}
	// European order: day first. The archive is French and English, and the US
	// month-first form is rare enough not to guess at.
	for _, m := range numRe.FindAllStringSubmatchIndex(text, -1) {
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		if y < 100 {
			y += 2000
		}
		hits = append(hits, dateHit{m[0], func() { add(y, mo, d) }})
	}
	for _, m := range textRe.FindAllStringSubmatchIndex(text, -1) {
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		name := removeAccents(text[m[4]:m[5]])
		mo, ok := months[name]
		if !ok {
			continue
		}
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		hits = append(hits, dateHit{m[0], func() { add(y, mo, d) }})
	}
	// Reading order, so the chips follow the page.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	for _, h := range hits {
		h.apply()
	}

	res.Titles = titles(text)
	return res
}

// titles picks the first few lines that read like a heading: enough letters, not
// mostly digits, and not too long.
func titles(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if len(out) >= maxTitles {
			break
		}
		if !looksLikeTitle(line) {
			continue
		}
		if len([]rune(line)) > maxTitleR {
			line = string([]rune(line)[:maxTitleR])
		}
		out = append(out, strings.Trim(line, " .-_:|"))
	}
	return out
}

func looksLikeTitle(s string) bool {
	letters, digits := 0, 0
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		}
	}
	return letters >= 6 && letters > digits
}

func removeAccents(s string) string {
	repl := strings.NewReplacer("é", "e", "è", "e", "ê", "e", "û", "u", "ô", "o", "î", "i", "â", "a", "ç", "c")
	return repl.Replace(strings.ToLower(s))
}
