package main

import "testing"

func TestParseMoneyCentsRejectsInvalidExpectations(t *testing.T) {
	valid := map[string]int64{"10": 1000, "10.0": 1000, "10.00": 1000, "1.": 100, "0": 0, "0.00": 0, "92233720368547758.07": 9223372036854775807}
	for text, want := range valid {
		got, err := parseMoneyCents(text)
		if err != nil || got != want {
			t.Errorf("parseMoneyCents(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	invalid := []string{"", ".", ".5", "1.-1", "-0.01", "+1.00", "1.001", "92233720368547758.08", "1x.00", "0x"}
	for _, text := range invalid {
		if _, err := parseMoneyCents(text); err == nil {
			t.Errorf("parseMoneyCents(%q) accepted invalid input", text)
		}
	}
}

func TestParseCountsRejectsMalformedExpectations(t *testing.T) {
	want, err := parseCounts("BET=33, OPENING=1")
	if err != nil || want["BET"] != 33 || want["OPENING"] != 1 {
		t.Fatalf("parseCounts valid = %v, %v", want, err)
	}
	for _, text := range []string{"BET=oops", "BET=", "=33", "BET=1,BET=2", "BET=-1"} {
		if _, err := parseCounts(text); err == nil {
			t.Errorf("parseCounts(%q) accepted invalid input", text)
		}
	}
}
