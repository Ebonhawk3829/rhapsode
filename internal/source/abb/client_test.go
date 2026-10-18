package abb

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// TestParseSearchPage_RedRising pins parsing against the live ABB search
// page for "red rising": GA dramatisations included, since that is the use
// case the scorer targets.
func TestParseSearchPage_RedRising(t *testing.T) {
	listings := parseSearchPage("https://audiobookbay.lu", fixture(t, "search_red_rising.html"))
	if len(listings) < 5 {
		t.Fatalf("got %d listings, want a page worth", len(listings))
	}
	var ga []listing
	for _, l := range listings {
		if strings.Contains(strings.ToLower(l.Title), "graphic audio") {
			ga = append(ga, l)
		}
	}
	if len(ga) == 0 {
		t.Errorf("no GraphicAudio listings parsed: %+v", listings)
	}
	for _, l := range ga {
		if !strings.HasPrefix(l.DetailPath, "/abss/") {
			t.Errorf("detail path %q is not /abss/", l.DetailPath)
		}
	}
}

// TestParseSearchPage_Fields pins the per-row metadata that feeds scoring.
func TestParseSearchPage_Fields(t *testing.T) {
	listings := parseSearchPage("https://audiobookbay.lu", fixture(t, "search_red_rising.html"))
	for _, l := range listings {
		if l.Format == "" || l.Posted == "" {
			t.Errorf("row %q lost format=%q posted=%q", l.Title, l.Format, l.Posted)
		}
		if l.BitrateKbps < 0 {
			t.Errorf("negative bitrate on %q", l.Title)
		}
		if l.SizeMB < 0 {
			t.Errorf("negative size on %q", l.Title)
		}
	}
}

// TestExtractInfoHash_HarryPotter pins hash extraction against a real detail
// page. This is the magnet's source of truth; losing it means ABB yields no
// usable candidate at all, so the failure has to be loud in tests.
func TestExtractInfoHash_HarryPotter(t *testing.T) {
	hash := extractInfoHash(fixture(t, "detail_harry_potter.html"))
	if len(hash) != 40 {
		t.Fatalf("hash = %q, want 40 hex chars", hash)
	}
	// The known value from the live page, probed 2026-10-18.
	if hash != "ffe7fd4194e791e65d026e92cb28e3af4e2c7072" {
		t.Errorf("hash = %q, want the live-page value", hash)
	}
}

func TestExtractInfoHash_RedRising(t *testing.T) {
	hash := extractInfoHash(fixture(t, "detail_red_rising_dramatized.html"))
	if len(hash) != 40 {
		t.Fatalf("hash = %q, want 40 hex chars", hash)
	}
}

func TestMagnetFor(t *testing.T) {
	m := magnetFor("FFE7FD4194E791E65D026E92CB28E3AF4E2C7072", "Complete Harry Potter")
	if !strings.Contains(m, "xt=urn:btih:ffe7fd4194e791e65d026e92cb28e3af4e2c7072") {
		t.Errorf("magnet lost or case-changed the hash: %s", m[:60])
	}
	if !strings.HasPrefix(m, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet prefix wrong: %s", m[:30])
	}
	if strings.Count(m, "&tr=") != len(defaultTrackers) {
		t.Errorf("tracker count = %d, want %d", strings.Count(m, "&tr="), len(defaultTrackers))
	}
	if !strings.Contains(m, "&dn=") {
		t.Error("magnet lost its display name")
	}
}

func TestParseBitrate(t *testing.T) {
	cases := map[string]int{
		"128 Kbps": 128,
		"64 kbps":  64,
		"VBR":      0,
		"Variable": 0,
	}
	for in, want := range cases {
		if got := parseBitrate(in); got != want {
			t.Errorf("parseBitrate(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSizeMB(t *testing.T) {
	cases := []struct {
		num, unit string
		want      int64
	}{
		{"7.42", "GBs", 7598},
		{"412.50", "MBs", 412},
		{"1.2", "TBs", 1258291},
		{"", "GBs", 0},
	}
	for _, c := range cases {
		if got := parseSizeMB(c.num, c.unit); got != c.want {
			t.Errorf("parseSizeMB(%q,%q) = %d, want %d", c.num, c.unit, got, c.want)
		}
	}
}
