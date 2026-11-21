package suggest

import "testing"

func TestDatesAreFoundInReadingOrderWithoutDuplicates(t *testing.T) {
	text := `Landlord Ltd
Rent review letter, dated 14 octobre 2026.
Effective from 2026-11-01. Copy of 14.10.2026 sent by post.
Invalid: 31/02/2026 should be ignored.`
	res := Analyse(text)

	var values []string
	for _, d := range res.Dates {
		values = append(values, d.Value)
	}
	want := []string{"2026-10-14", "2026-11-01"}
	if len(values) != len(want) || values[0] != want[0] || values[1] != want[1] {
		t.Fatalf("dates = %v, want %v", values, want)
	}
	if res.Dates[0].Label != "14 Oct 2026" || res.Dates[0].Source != "text" {
		t.Fatalf("first date = %+v", res.Dates[0])
	}
}

func TestEnglishTextualDates(t *testing.T) {
	res := Analyse("Statement issued 3 March 2025 for account 10043.")
	if len(res.Dates) != 1 || res.Dates[0].Value != "2025-03-03" || res.Dates[0].Label != "3 Mar 2025" {
		t.Fatalf("dates = %+v", res.Dates)
	}
}

func TestTitlesSkipNumbersAndShortLines(t *testing.T) {
	text := "12345 67890\nAB\nFacture d'électricité\nTotal 88,00 EUR\nLandlord letter, rent review"
	res := Analyse(text)
	if len(res.Titles) < 2 || res.Titles[0] != "Facture d'électricité" || res.Titles[1] != "Total 88,00 EUR" {
		t.Fatalf("titles = %q", res.Titles)
	}
}

func TestNoTextGivesEmptySuggestions(t *testing.T) {
	res := Analyse("")
	if res.Dates == nil || res.Titles == nil || len(res.Dates) != 0 || len(res.Titles) != 0 {
		t.Fatalf("result = %+v, want empty slices", res)
	}
}
