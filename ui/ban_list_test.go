package ui

import (
	"strings"
	"testing"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestBanListEntryText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ban  pb.BanInfo
		want string
	}{
		{name: "named account", ban: pb.BanInfo{UserID: 7, Username: "banned-user"}, want: "Account: banned-user - permanent"},
		{name: "legacy account fallback", ban: pb.BanInfo{UserID: 7}, want: "Account #7 - permanent"},
		{name: "exact IP", ban: pb.BanInfo{IP: "192.0.2.1"}, want: "Exact IP: 192.0.2.1 - permanent"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := banListEntryText(tc.ban)
			if got != tc.want {
				t.Fatalf("banListEntryText(%#v) = %q, want %q", tc.ban, got, tc.want)
			}
			if strings.Contains(got, "—") {
				t.Fatalf("ban list entry contains an em dash: %q", got)
			}
		})
	}
}
