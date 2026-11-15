package datastore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestConcurrentChannelAdmissionSharesWireBudget(t *testing.T) {
	f := newChannelUniquenessStore(t)
	remaining := model.MaxChannelStateBytes
	for i := 0; ; i++ {
		ch := &model.Channel{Name: strings.Repeat("x", 60) + fmt.Sprintf("%04d", i), Description: strings.Repeat("&", 256)}
		cost := model.ChannelStateBytes(*ch)
		if remaining < cost {
			break
		}
		if err := f.NonTx().CreateChannel(ch); err != nil {
			t.Fatal(err)
		}
		remaining -= cost
	}
	cost := model.ChannelStateBytes(model.Channel{Name: "false"})
	if remaining >= 2*cost {
		runes := (remaining - cost - model.StateChannelFixedBytes) / model.StateJSONRuneBytes
		filler := &model.Channel{Name: strings.Repeat("z", min(runes, model.MaxChannelNameLength)), Description: strings.Repeat("z", max(0, runes-model.MaxChannelNameLength))}
		if err := f.NonTx().CreateChannel(filler); err != nil {
			t.Fatal(err)
		}
		remaining -= model.ChannelStateBytes(*filler)
	}
	if remaining < cost || remaining >= 2*cost {
		t.Fatalf("fixture requires exactly one slot, remaining %d", remaining)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, temporary := range []bool{false, true} {
		go func() {
			<-start
			results <- f.NonTx().CreateChannel(&model.Channel{Name: fmt.Sprint(temporary), IsTemp: temporary})
		}()
	}
	close(start)
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent successful admissions = %d, want 1", succeeded)
	}
	channels, err := f.NonTx().ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, ch := range channels {
		total += model.ChannelStateBytes(ch)
	}
	if total > model.MaxChannelStateBytes {
		t.Fatalf("persisted cost %d exceeds %d", total, model.MaxChannelStateBytes)
	}
}
