package protocol

const replayWindowBits = 64

// ReplayWindow accepts each positive, non-wrapping uint32 sequence at most once
// while allowing bounded reordering. Callers must authenticate a packet before
// mutating the window so forged high sequences cannot suppress valid traffic.
type ReplayWindow struct {
	highest     uint32
	seen        uint64
	initialized bool
}

// Accept records sequence and reports whether it is fresh and within the
// 64-packet window. Sequence zero and uint32 wrap are rejected by design.
func (w *ReplayWindow) Accept(sequence uint32) bool {
	if sequence == 0 {
		return false
	}
	if !w.initialized {
		w.highest = sequence
		w.seen = 1
		w.initialized = true
		return true
	}
	if sequence > w.highest {
		advance := sequence - w.highest
		if advance >= replayWindowBits {
			w.seen = 1
		} else {
			w.seen = (w.seen << advance) | 1
		}
		w.highest = sequence
		return true
	}

	age := w.highest - sequence
	if age >= replayWindowBits {
		return false
	}
	mask := uint64(1) << age
	if w.seen&mask != 0 {
		return false
	}
	w.seen |= mask
	return true
}
