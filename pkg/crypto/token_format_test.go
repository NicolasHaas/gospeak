package crypto

import (
	"strings"
	"testing"
)

func TestGeneratedTokenFormat(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !IsGeneratedToken(token) {
		t.Fatal("generated token rejected")
	}
	for _, bad := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("0", 63) + "\n"} {
		if IsGeneratedToken(bad) {
			t.Fatal("invalid new credential accepted")
		}
	}
}
