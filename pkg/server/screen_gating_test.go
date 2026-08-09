package server

import "testing"

func TestRunningLogGatesScreenAddress(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := DefaultConfig()
		cfg.EnableScreenShare = enabled
		args := New(cfg, Dependencies{}).runningLogArgs()

		found := false
		for i := 0; i+1 < len(args); i += 2 {
			if args[i] == "screen" {
				found = true
				if args[i+1] != cfg.ScreenAddr {
					t.Fatalf("screen log address = %v, want %q", args[i+1], cfg.ScreenAddr)
				}
			}
		}
		if found != enabled {
			t.Fatalf("screen log field present = %t with screen sharing enabled = %t", found, enabled)
		}
	}
}
