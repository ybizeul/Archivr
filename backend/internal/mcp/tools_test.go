package mcp

import "testing"

func TestSlicePageCountsCharacters(t *testing.T) {
	const s = "aœb€cd" // œ is 2 bytes and € is 3, but each is one character
	cases := []struct {
		offset, n int
		page      string
		truncated bool
	}{
		{0, 100, s, false},
		{0, 2, "aœ", true},
		{2, 2, "b€", true},
		{4, 2, "cd", false},
		{5, 1, "d", false},
		{6, 5, "", false},
		{99, 5, "", false},
		{1, 0, "", true},
	}
	for _, c := range cases {
		page, total, truncated := slicePage(s, c.offset, c.n)
		if page != c.page || truncated != c.truncated || total != 6 {
			t.Errorf("slicePage(%d, %d) = %q, %d, %v; want %q, 6, %v", c.offset, c.n, page, total, truncated, c.page, c.truncated)
		}
	}
}

func TestMarkSnippet(t *testing.T) {
	if got := markSnippet("a \x01rent\x02 review"); got != "a **rent** review" {
		t.Errorf("markSnippet = %q", got)
	}
}
