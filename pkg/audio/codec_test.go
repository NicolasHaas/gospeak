package audio

import "testing"

func TestDecoderPLCLossAndRecovery(t *testing.T) {
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, opusFrameSize)
	for i := range frame {
		frame[i] = int16((i%48)*200 - 4800)
	}
	packet, err := enc.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if pcm, err := dec.Decode(packet); err != nil || len(pcm) != opusFrameSize {
		t.Fatalf("seed decode: samples=%d err=%v", len(pcm), err)
	}
	for loss := 1; loss <= 3; loss++ {
		pcm, err := dec.DecodePLC()
		if err != nil || len(pcm) != opusFrameSize {
			t.Fatalf("loss %d: samples=%d err=%v, want %d samples without error", loss, len(pcm), err, opusFrameSize)
		}
	}
	packet, err = enc.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if pcm, err := dec.Decode(packet); err != nil || len(pcm) != opusFrameSize {
		t.Fatalf("recovery decode: samples=%d err=%v", len(pcm), err)
	}
}
