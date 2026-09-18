package model

import "testing"

func TestCanonicalIPAddress(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "IPv4", input: "192.0.2.10", want: "192.0.2.10"},
		{name: "mapped IPv4", input: "::ffff:192.0.2.10", want: "192.0.2.10"},
		{name: "IPv6", input: "2001:0db8:0:0::1", want: "2001:db8::1"},
		{name: "CIDR", input: "192.0.2.0/24", wantErr: true},
		{name: "hostname", input: "example.invalid", wantErr: true},
		{name: "port", input: "192.0.2.10:9600", wantErr: true},
		{name: "zone", input: "fe80::1%eth0", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalIPAddress(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CanonicalIPAddress(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalIPAddress(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalIPAddress(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
