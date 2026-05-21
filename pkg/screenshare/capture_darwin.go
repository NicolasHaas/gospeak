//go:build darwin

package screenshare

import (
	"fmt"
	"image"
	"strings"

	"github.com/kbinani/screenshot"
)

var defaultBackend CaptureBackend = screencaptureBackend{}

type screencaptureBackend struct{}

func (screencaptureBackend) CaptureDisplay(displayIndex int) (*image.RGBA, error) {
	if displayIndex < 0 || displayIndex >= screenshot.NumActiveDisplays() {
		return nil, fmt.Errorf("display %d not available", displayIndex)
	}
	bounds := screenshot.GetDisplayBounds(displayIndex)
	img, err := screenshot.CaptureRect(bounds)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "cannot capture display") {
			return nil, fmt.Errorf("cannot capture display; check macOS Screen Recording permission and restart the client")
		}
		return nil, err
	}
	return img, nil
}
