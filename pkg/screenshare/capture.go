package screenshare

import "image"

type CaptureBackend interface {
	CaptureDisplay(displayIndex int) (*image.RGBA, error)
}

func CaptureDisplay(displayIndex int) (*image.RGBA, error) {
	return defaultBackend.CaptureDisplay(displayIndex)
}
