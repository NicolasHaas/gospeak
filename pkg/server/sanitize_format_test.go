package server

import (
	"strings"
	"testing"
	"unicode"
)

func TestSanitizeTextStripsFormatCharacters(t *testing.T) {
	// Bidi overrides and invisible joiners can visually reorder or hide text in
	// Unicode-aware clients; channel validation already rejects them.
	hostile := "abc\u202Edef RLO\u202Bpar\u202Cend ZWSP\u200B ZWNJ\u200C LRM\u200E RLM\u200F"
	got := sanitizeText(hostile)
	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("format character %U survived sanitization in %q", r, got)
		}
	}
	if strings.ContainsAny(got, "\u202E\u202A\u202B\u202C\u202D\u200B\u200C\u200D\u200E\u200F") {
		t.Fatalf("known bidi/invisible characters survived: %q", got)
	}
	if got != "abcdef RLOparend ZWSP ZWNJ LRM RLM" {
		t.Fatalf("unexpected sanitized output: %q", got)
	}
	if sanitizeText("plain text") != "plain text" {
		t.Fatal("plain text changed")
	}
	if got := sanitizeText("a\nb\rc"); got != "a b c" {
		t.Fatalf("newline collapsing changed: %q", got)
	}
}
