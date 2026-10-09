//go:build !darwin

package screenshare

import (
	"fmt"
	"image"

	"github.com/kbinani/screenshot"
)

func CaptureDisplay(displayIndex int) (*image.RGBA, error) {
	if displayIndex < 0 || displayIndex >= screenshot.NumActiveDisplays() {
		return nil, fmt.Errorf("display %d not available", displayIndex)
	}
	bounds := screenshot.GetDisplayBounds(displayIndex)
	return screenshot.CaptureRect(bounds)
}
