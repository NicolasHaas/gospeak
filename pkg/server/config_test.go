package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
)

func TestImportChannelsFromYAMLRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown root field":   "channels: []\nunexpected: true\n",
		"unknown nested field": "channels:\n  - name: Lobby\n    unexpected: true\n",
		"extra document":       "channels: []\n---\nchannels: []\n",
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			st := newChannelImportStore(t)
			if err := ImportChannelsFromYAML([]byte(input), st); err == nil {
				t.Fatal("ImportChannelsFromYAML() error = nil, want invalid YAML error")
			}
		})
	}
}

func TestImportChannelsFromYAMLRejectsOversizedAndAliasedInput(t *testing.T) {
	tests := map[string][]byte{
		"oversized":          []byte("channels: []\n#" + strings.Repeat("x", 512*1024)),
		"alias":              []byte("channels:\n  - &shared\n    name: shared\n  - *shared\n"),
		"binary name":        []byte("channels:\n  - name: !!binary c2FmZf8=\n"),
		"binary description": []byte("channels:\n  - name: safe\n    description: !!binary YmFk/w==\n"),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			st := newChannelImportStore(t)
			if err := ImportChannelsFromYAML(input, st); err == nil {
				t.Fatal("ImportChannelsFromYAML() error = nil, want bounded parser error")
			}
			assertNoImportedChannels(t, st)
		})
	}
}

func TestImportChannelsFromYAMLRequiresExplicitChannelsSequence(t *testing.T) {
	for name, input := range map[string]string{
		"null document":   "null\n",
		"empty mapping":   "{}\n",
		"null channels":   "channels:\n",
		"empty document":  "---\n",
		"scalar channels": "channels: invalid\n",
	} {
		t.Run(name, func(t *testing.T) {
			st := newChannelImportStore(t)
			if err := ImportChannelsFromYAML([]byte(input), st); err == nil {
				t.Fatal("ImportChannelsFromYAML() error = nil, want document shape error")
			}
			assertNoImportedChannels(t, st)
		})
	}

	t.Run("explicit empty sequence", func(t *testing.T) {
		st := newChannelImportStore(t)
		if err := ImportChannelsFromYAML([]byte("channels: []\n"), st); err != nil {
			t.Fatalf("ImportChannelsFromYAML() error = %v, want success", err)
		}
		assertNoImportedChannels(t, st)
	})
}

func TestImportChannelsFromYAMLRejectsDepthAndCountOverLimits(t *testing.T) {
	t.Run("maximum depth accepted", func(t *testing.T) {
		if _, err := parseChannelsYAML([]byte(nestedChannelsYAML(8))); err != nil {
			t.Fatalf("parseChannelsYAML() error = %v, want 8 levels accepted", err)
		}
	})

	t.Run("depth", func(t *testing.T) {
		st := newChannelImportStore(t)
		if err := ImportChannelsFromYAML([]byte(nestedChannelsYAML(9)), st); err == nil {
			t.Fatal("ImportChannelsFromYAML() error = nil, want depth limit error")
		}
		assertNoImportedChannels(t, st)
	})

	channelsYAML := func(count int) []byte {
		var input strings.Builder
		input.WriteString("channels:\n")
		for i := 0; i < count; i++ {
			fmt.Fprintf(&input, "  - name: channel-%d\n", i)
		}
		return []byte(input.String())
	}

	t.Run("maximum count accepted", func(t *testing.T) {
		if _, err := parseChannelsYAML(channelsYAML(256)); err != nil {
			t.Fatalf("parseChannelsYAML() error = %v, want 256 channels accepted", err)
		}
	})

	t.Run("count", func(t *testing.T) {
		st := newChannelImportStore(t)
		if err := ImportChannelsFromYAML(channelsYAML(257), st); err == nil {
			t.Fatal("ImportChannelsFromYAML() error = nil, want channel count limit error")
		}
		assertNoImportedChannels(t, st)
	})
}

func TestImportChannelsFromYAMLValidatesBeforeApplying(t *testing.T) {
	st := newChannelImportStore(t)
	input := []byte("channels:\n  - name: valid\n  - name: ''\n")

	if err := ImportChannelsFromYAML(input, st); err == nil {
		t.Fatal("ImportChannelsFromYAML() error = nil, want channel validation error")
	}
	assertNoImportedChannels(t, st)
}

func TestImportChannelsFromYAMLRejectsDuplicateSiblingDeclarations(t *testing.T) {
	st := newChannelImportStore(t)
	input := []byte("channels:\n  - name: duplicate\n    description: first\n  - name: duplicate\n    description: second\n")

	if err := ImportChannelsFromYAML(input, st); err == nil {
		t.Fatal("ImportChannelsFromYAML() error = nil, want duplicate sibling error")
	}
	assertNoImportedChannels(t, st)
}

func nestedChannelsYAML(levels int) string {
	var input strings.Builder
	input.WriteString("channels:\n")
	for depth := 0; depth < levels; depth++ {
		input.WriteString(strings.Repeat("  ", depth+1))
		input.WriteString("- name: channel\n")
		if depth < levels-1 {
			input.WriteString(strings.Repeat("  ", depth+2))
			input.WriteString("channels:\n")
		}
	}
	return input.String()
}

func newChannelImportStore(t *testing.T) *datastore.ProviderFactory {
	t.Helper()
	st, err := datastore.NewProviderFactory(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatalf("open datastore: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close datastore: %v", err)
		}
	})
	return st
}

func assertNoImportedChannels(t *testing.T, st *datastore.ProviderFactory) {
	t.Helper()
	channels, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("imported channels = %#v, want none", channels)
	}
}
