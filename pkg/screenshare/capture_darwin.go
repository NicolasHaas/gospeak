//go:build darwin

package screenshare

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strconv"
)

var defaultBackend CaptureBackend = screencaptureBackend{}

type screencaptureBackend struct{}

func (screencaptureBackend) CaptureDisplay(displayIndex int) (*image.RGBA, error) {
	tempFile, err := os.CreateTemp("", "gospeak-screen-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp capture file: %w", err)
	}
	filePath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		return nil, fmt.Errorf("close temp capture file: %w", err)
	}
	defer func() {
		_ = os.Remove(filePath)
	}()

	ctx := context.Background()
	//nolint:gosec // command name and arguments are fully controlled locally.
	cmd := exec.CommandContext(ctx, "screencapture", "-x", "-D", strconv.Itoa(displayIndex+1), filePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture failed: %w: %s", err, string(output))
	}

	//nolint:gosec // filePath is a locally created temporary file path.
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open capture: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode capture: %w", err)
	}

	rgba := image.NewRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba, nil
}
