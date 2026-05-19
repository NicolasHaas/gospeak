//go:build !darwin

package screenshare

import (
	"fmt"
	"image"

	"github.com/kbinani/screenshot"
)

var defaultBackend CaptureBackend = screenshotBackend{}

type screenshotBackend struct{}

func (screenshotBackend) CaptureDisplay(displayIndex int) (*image.RGBA, error) {
	if displayIndex < 0 || displayIndex >= screenshot.NumActiveDisplays() {
		return nil, fmt.Errorf("display %d not available", displayIndex)
	}
	bounds := screenshot.GetDisplayBounds(displayIndex)
	return screenshot.CaptureRect(bounds)
}
