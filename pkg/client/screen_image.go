package client

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

const (
	maxScreenImageDimension = 8192
	maxScreenImagePixels    = 16 * 1024 * 1024
)

func decodeScreenImage(frame *protocol.ScreenFrame) (image.Image, error) {
	if frame == nil || frame.Format != "jpeg" || frame.Width <= 0 || frame.Height <= 0 || frame.Width > maxScreenImageDimension || frame.Height > maxScreenImageDimension || int64(frame.Width)*int64(frame.Height) > maxScreenImagePixels {
		return nil, fmt.Errorf("invalid screen image metadata")
	}
	reader := bytes.NewReader(frame.Data)
	cfg, err := jpeg.DecodeConfig(reader)
	if err != nil {
		return nil, fmt.Errorf("read screen JPEG dimensions: %w", err)
	}
	// Check the JPEG's own dimensions before its decoder allocates the canvas.
	if cfg.Width != int(frame.Width) || cfg.Height != int(frame.Height) {
		return nil, fmt.Errorf("screen JPEG dimensions do not match frame metadata")
	}
	if _, err := reader.Seek(0, 0); err != nil {
		return nil, err
	}
	img, err := jpeg.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("decode screen JPEG: %w", err)
	}
	if img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
		return nil, fmt.Errorf("decoded screen JPEG dimensions changed")
	}
	return img, nil
}
