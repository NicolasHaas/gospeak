package audio

import (
	"reflect"
	"testing"
)

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
