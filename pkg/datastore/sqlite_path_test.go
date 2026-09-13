package datastore

import (
	"net/url"
	"testing"
)

func TestSQLiteFileURIWindowsPaths(t *testing.T) {
	tests := map[string]struct {
		uri  string
		want string
	}{
		"canonical drive": {uri: "file:///C:/data/gospeak.db", want: `C:\data\gospeak.db`},
		"opaque drive":    {uri: "file:C:/data/gospeak.db", want: `C:\data\gospeak.db`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			uri, err := url.Parse(tc.uri)
			if err != nil {
				t.Fatalf("parse URI: %v", err)
			}
			got, err := sqliteFileURIPath(uri, "windows")
			if err != nil {
				t.Fatalf("sqliteFileURIPath: %v", err)
			}
			if got != tc.want {
				t.Fatalf("sqliteFileURIPath(%q) = %q, want %q", tc.uri, got, tc.want)
			}
		})
	}
}
