package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
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

func TestImportChannelsFromYAMLRollsBackOnStoreFailure(t *testing.T) {
	st := newChannelImportStore(t)
	if _, err := st.DB.Exec(`CREATE TRIGGER reject_broken_channel
		BEFORE INSERT ON channels WHEN NEW.name = 'broken'
		BEGIN SELECT RAISE(FAIL, 'injected channel failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	input := []byte("channels:\n  - name: valid\n  - name: broken\n")

	if err := ImportChannelsFromYAML(input, st); err == nil {
		t.Fatal("ImportChannelsFromYAML() error = nil, want store error")
	}
	assertNoImportedChannels(t, st)
}

func TestImportChannelsFromYAMLPreservesExistingChannels(t *testing.T) {
	st := newChannelImportStore(t)
	original := []byte("channels:\n  - name: existing\n    description: original\n    max_users: 5\n    channels:\n      - name: child\n        description: child-original\n        max_users: 3\n")
	if err := ImportChannelsFromYAML(original, st); err != nil {
		t.Fatalf("initial import: %v", err)
	}
	parentBefore, err := st.NonTx().GetChannelByNameAndParent("existing", 0)
	if err != nil || parentBefore == nil {
		t.Fatalf("get original parent: channel = %#v, error = %v", parentBefore, err)
	}
	childBefore, err := st.NonTx().GetChannelByNameAndParent("child", parentBefore.ID)
	if err != nil || childBefore == nil {
		t.Fatalf("get original child: channel = %#v, error = %v", childBefore, err)
	}

	changed := []byte("channels:\n  - name: existing\n    description: replacement\n    max_users: 10\n    channels:\n      - name: child\n        description: child-replacement\n        max_users: 8\n        channels:\n          - name: grandchild\n  - name: child\n")
	if err := ImportChannelsFromYAML(changed, st); err != nil {
		t.Fatalf("second import: %v", err)
	}

	parentAfter, err := st.NonTx().GetChannelByNameAndParent("existing", 0)
	if err != nil {
		t.Fatalf("get existing channel: %v", err)
	}
	if parentAfter == nil || parentAfter.ID != parentBefore.ID || parentAfter.Description != "original" || parentAfter.MaxUsers != 5 {
		t.Fatalf("existing parent = %#v, want original channel %#v preserved", parentAfter, parentBefore)
	}
	childAfter, err := st.NonTx().GetChannelByNameAndParent("child", parentAfter.ID)
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if childAfter == nil || childAfter.ID != childBefore.ID || childAfter.Description != "child-original" || childAfter.MaxUsers != 3 {
		t.Fatalf("existing child = %#v, want original channel %#v preserved", childAfter, childBefore)
	}
	grandchild, err := st.NonTx().GetChannelByNameAndParent("grandchild", childAfter.ID)
	if err != nil || grandchild == nil {
		t.Fatalf("get missing descendant after import: channel = %#v, error = %v", grandchild, err)
	}
	rootChild, err := st.NonTx().GetChannelByNameAndParent("child", 0)
	if err != nil || rootChild == nil || rootChild.ID == childAfter.ID {
		t.Fatalf("same-name root channel = %#v, nested child = %#v, error = %v", rootChild, childAfter, err)
	}
}

func TestImportChannelsFromYAMLConcurrentImportsDoNotDuplicateSiblings(t *testing.T) {
	st := newChannelImportStore(t)
	input := []byte("channels:\n  - name: shared\n")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- ImportChannelsFromYAML(input, st)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent idempotent import failed: %v", err)
		}
	}
	channels, err := st.NonTx().ListChannels()
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(channels) != 1 || channels[0].Name != "shared" {
		t.Fatalf("channels = %#v, want one shared sibling", channels)
	}
}

func TestHandleImportChannelsReturnsGenericFailure(t *testing.T) {
	srv, st, handler := newTestServer(t)
	conn := &bufferConn{}
	session := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)

	srv.handleImportChannels(session.ID, &pb.ImportChannelsRequest{
		YAML: "channels:\n  - name: invalid\n    internal_path: /private/operator/path\n",
	}, st, conn, handler)

	response, err := protocol.ReadControlMessage(conn)
	if err != nil {
		t.Fatalf("read import response: %v", err)
	}
	if response.ImportChannelsResp == nil {
		t.Fatalf("import response = %#v, want ImportChannelsResponse", response)
	}
	if response.ImportChannelsResp.Success || response.ImportChannelsResp.Message != "channel import failed" {
		t.Fatalf("import response = %#v, want generic failure", response.ImportChannelsResp)
	}
}

func TestRunRollsBackLobbyWhenChannelInitializationFails(t *testing.T) {
	st := newChannelImportStore(t)
	if _, err := st.DB.Exec(`CREATE TRIGGER reject_broken_startup_channel
		BEFORE INSERT ON channels WHEN NEW.name = 'broken'
		BEGIN SELECT RAISE(FAIL, 'injected startup failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	channelsPath := filepath.Join(t.TempDir(), "channels.yaml")
	if err := os.WriteFile(channelsPath, []byte("channels:\n  - name: valid\n  - name: broken\n"), 0o600); err != nil {
		t.Fatalf("write channels config: %v", err)
	}
	cfg := DefaultConfig()
	cfg.ChannelsFile = channelsPath
	cfg.ControlAddr = "invalid-address"
	srv := New(cfg, Dependencies{Store: st})

	err := srv.Run()
	if err == nil || !strings.Contains(err.Error(), "initialize channels") {
		t.Fatalf("Run() error = %v, want channel initialization error", err)
	}
	assertNoImportedChannels(t, st)
}

func TestRunFailsForExplicitMissingChannelsFile(t *testing.T) {
	st := newChannelImportStore(t)
	cfg := DefaultConfig()
	cfg.ChannelsFile = filepath.Join(t.TempDir(), "missing.yaml")
	cfg.ControlAddr = "invalid-address"
	srv := New(cfg, Dependencies{Store: st})

	err := srv.Run()
	if err == nil || !strings.Contains(err.Error(), "load channels config") {
		t.Fatalf("Run() error = %v, want channels config startup error", err)
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
