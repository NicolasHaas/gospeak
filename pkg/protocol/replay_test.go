package protocol

import (
	"math"
	"testing"
)

func TestReplayWindowAcceptsBoundedReorderingOnce(t *testing.T) {
	var window ReplayWindow
	for _, sequence := range []uint32{10, 12, 11, 9} {
		if !window.Accept(sequence) {
			t.Fatalf("Accept(%d) = false, want true", sequence)
		}
		if window.Accept(sequence) {
			t.Fatalf("second Accept(%d) = true, want replay rejected", sequence)
		}
	}
}

func TestReplayWindowRejectsZeroAndPacketsOutsideWindow(t *testing.T) {
	var window ReplayWindow
	if window.Accept(0) {
		t.Fatal("Accept(0) = true, want reserved sequence rejected")
	}
	if !window.Accept(1) || !window.Accept(65) {
		t.Fatal("failed to establish replay window")
	}
	if window.Accept(1) {
		t.Fatal("packet 64 behind highest sequence was accepted")
	}
	if !window.Accept(2) {
		t.Fatal("packet 63 behind highest sequence was rejected")
	}
}

func TestReplayWindowDoesNotTreatUint32WrapAsFresh(t *testing.T) {
	var window ReplayWindow
	if !window.Accept(math.MaxUint32-1) || !window.Accept(math.MaxUint32) {
		t.Fatal("failed to accept final non-wrapping sequence values")
	}
	if window.Accept(0) || window.Accept(1) {
		t.Fatal("wrapped sequence was accepted as fresh")
	}
}
