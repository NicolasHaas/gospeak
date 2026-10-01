package server

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func FuzzChatTextSanitization(f *testing.F) {
	for _, text := range []string{"hello", "  héllo 👋  ", "a\x00b\n\x1b[31m", strings.Repeat("界", 2001), string([]byte{0xff, 0xfe})} {
		f.Add([]byte(text))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 8192 {
			return
		}
		text := sanitizeText(strings.TrimSpace(string(input)))
		if !utf8.ValidString(text) || sanitizeText(text) != text {
			t.Fatal("chat text sanitization produced invalid or unstable text")
		}
		for _, r := range text {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Fatalf("chat text retained control or format character %U", r)
			}
		}
	})
}
