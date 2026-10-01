package audio

import "testing"

func TestVADNegativeFrameCounts(t *testing.T) {
	v := NewVAD(200, -1, -1)
	if v.holdTime != 0 || v.preBufSize != 0 {
		t.Errorf("negative counts retained")
	}
	if !v.Process([]int16{1000}) || v.Process([]int16{0}) || len(v.PreBufferedFrames()) != 0 {
		t.Fatal("negative configuration did not behave as zero")
	}
}
