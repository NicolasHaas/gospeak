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
	if maxWidth <= 0 {
		maxWidth = DefaultMaxWidth
	}
	if quality <= 0 || quality > 100 {
		quality = DefaultJPEGQuality
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	scaled := img
	if width > maxWidth {
		targetHeight := height * maxWidth / width
		dst := image.NewRGBA(image.Rect(0, 0, maxWidth, targetHeight))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		scaled = dst
		width = maxWidth
		height = targetHeight
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	if width < 0 || width > math.MaxInt32 || height < 0 || height > math.MaxInt32 {
		return nil, 0, 0, fmt.Errorf("screen image dimensions out of range: %dx%d", width, height)
	}
	return buf.Bytes(), int32(width), int32(height), nil
}
