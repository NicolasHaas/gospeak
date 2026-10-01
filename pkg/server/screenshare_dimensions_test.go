package server

import "testing"

func TestScreenShareDimensionBounds(t *testing.T) {
	for _, dims := range [][2]int32{{8193, 1}, {1, 8193}, {0, 1}, {1, -1}} {
		manager := NewScreenShareManager()
		if _, err := manager.Start(1, 1, 1, "sharer", dims[0], dims[1]); err == nil {
			t.Errorf("accepted dimensions %v", dims)
		}
	}
	manager := NewScreenShareManager()
	if _, err := manager.Start(1, 1, 1, "sharer", 8192, 1); err != nil {
		t.Fatal(err)
	}
}
