package client

import "testing"

func TestResolveAdvertisedAddr(t *testing.T) {
	tests := []struct {
		name           string
		controlAddr    string
		advertisedAddr string
		defaultPort    string
		want           string
		wantErr        bool
	}{
		{"empty advertised uses control host", "example.com:9600", "", "9603", "example.com:9603", false},
		{"wildcard host uses control host", "127.0.0.1:9600", "0.0.0.0:9603", "9603", "127.0.0.1:9603", false},
		{"explicit host preserved", "127.0.0.1:9600", "10.0.0.2:9603", "9603", "10.0.0.2:9603", false},
		{"bad control address", "bad", "", "9603", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAdvertisedAddr(tt.controlAddr, tt.advertisedAddr, tt.defaultPort)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveAdvertisedAddr() err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveAdvertisedAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}
