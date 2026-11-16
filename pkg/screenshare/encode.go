package screenshare

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"golang.org/x/image/draw"
)

const (
	DefaultJPEGQuality = 60
	DefaultMaxWidth    = 1280
)

func EncodeJPEG(img image.Image, maxWidth int, quality int) ([]byte, int32, int32, error) {
	if maxWidth <= 0 || maxWidth > DefaultMaxWidth {
		maxWidth = DefaultMaxWidth
	}
	if quality <= 0 || quality > 100 {
		quality = DefaultJPEGQuality
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("screen image dimensions out of range: %dx%d", width, height)
	}
	// Fit both orientations within the receiver's canvas budget, without enlargement.
	scale := math.Min(float64(maxWidth)/float64(width), float64(DefaultMaxWidth)/float64(height))
	scaled := img
	if scale < 1 {
		targetWidth := max(1, int(float64(width)*scale))
		targetHeight := max(1, int(float64(height)*scale))
		dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		scaled = dst
		width = targetWidth
		height = targetHeight
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	if width < 0 || width > math.MaxInt32 || height < 0 || height > math.MaxInt32 {
		return nil, 0, 0, fmt.Errorf("screen image dimensions out of range: %dx%d", width, height)
	}
	//nolint:gosec // width and height are range-checked against MaxInt32 above.
	return buf.Bytes(), int32(width), int32(height), nil
}
