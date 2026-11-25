package client

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestDocumentedStateAndReceiverLimits(t *testing.T) {
	for _, tc := range []struct {
		file    string
		phrases []string
	}{
		{"channels.md", []string{fmt.Sprintf("not exceed %d bytes", model.MaxChannelStateBytes), fmt.Sprintf("another %d bytes", model.StateUserBytes)}},
		{"security.md", []string{fmt.Sprintf("no larger than %d per axis", maxScreenImageDimension), fmt.Sprintf("%d pixels in total", maxScreenImagePixels)}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile("../../docs/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			for _, phrase := range tc.phrases {
				if !strings.Contains(strings.ReplaceAll(string(data), ",", ""), phrase) {
					t.Errorf("documentation missing %q", phrase)
				}
			}
		})
	}
	t.Logf("session=%d channel=%d receiver axis=%d pixels=%d", model.StateUserBytes, model.MaxChannelStateBytes, maxScreenImageDimension, maxScreenImagePixels)
}
