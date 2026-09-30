package ui

import (
	"testing"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestFlattenChannelsRejectsDuplicateIDs(t *testing.T) {
	a := &App{channels: []pb.ChannelInfo{{ID: 1}, {ID: 1}}}
	if items := a.flattenChannels(); len(items) != 1 {
		t.Fatalf("duplicate IDs produced %d rows", len(items))
	}
}
func TestFlattenChannelsMalformedGraph(t *testing.T) {
	for _, channels := range [][]pb.ChannelInfo{
		{{ID: 1}, {ID: 2, ParentID: 1}, {ID: 1, ParentID: 2}},
		{{ID: 0}, {ID: 1, ParentID: 0}},
		{{ID: 1, ParentID: 2}, {ID: 2, ParentID: 1}},
	} {
		items := (&App{channels: channels}).flattenChannels()
		seen := map[int64]bool{}
		for _, item := range items {
			if item.channelID == 0 || seen[item.channelID] {
				t.Fatal("invalid/repeated channel", item.channelID)
			}
			seen[item.channelID] = true
		}
	}
}
