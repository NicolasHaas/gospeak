package audio

import (
	"reflect"
	"testing"
)

func TestMixFramesSingleSpeakerFrameSize(t *testing.T) {
	for _, tt := range []struct {
		name  string
		frame []int16
		size  int
		want  []int16
	}{
		{"empty", nil, 3, []int16{0, 0, 0}},
		{"short", []int16{123, -456}, 3, []int16{123, -456, 0}},
		{"exact", []int16{123, -456, 789}, 3, []int16{123, -456, 789}},
		{"long", []int16{123, -456, 789, 1000}, 3, []int16{123, -456, 789}},
		{"zero", []int16{123}, 0, []int16{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := MixFrames([][]int16{tt.frame}, tt.size)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MixFrames() = %v, want %v", got, tt.want)
			}
			if len(tt.frame) == tt.size && len(got) > 0 && &got[0] != &tt.frame[0] {
				t.Fatal("exact-size frame should retain the allocation-free fast path")
			}
		})
	}
}

func TestMixFramesSaturatesAndTreatsMissingSamplesAsSilence(t *testing.T) {
	frames := [][]int16{
		{20000, -20000, 1000},
		{20000, -20000},
	}

	got := MixFrames(frames, 3)
	want := []int16{32767, -32768, 1000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MixFrames() = %v, want %v", got, want)
	}
}
