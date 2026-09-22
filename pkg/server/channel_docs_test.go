package server

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestChannelDocumentationExamplesParse(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate package working directory: %v", err)
	}
	path := filepath.Join(workingDirectory, "..", "..", "docs", "channels.md")
	data, err := os.ReadFile(path) //nolint:gosec // Test reads a fixed repository file relative to the package working directory.
	if err != nil {
		t.Fatalf("read docs/channels.md: %v", err)
	}

	blocks := regexp.MustCompile("(?s)```yaml channels-config\\n(.*?)\\n```").FindAllSubmatch(data, -1)
	if len(blocks) != 2 {
		t.Fatalf("docs/channels.md contains %d validated channel configuration examples, want 2", len(blocks))
	}
	for i, block := range blocks {
		if _, err := parseChannelsYAML(block[1]); err != nil {
			t.Errorf("channel configuration example %d: %v", i+1, err)
		}
	}
}
