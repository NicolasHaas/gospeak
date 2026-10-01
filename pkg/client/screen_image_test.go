package client

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

func screenTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeScreenImageRejectsUntrustedMetadata(t *testing.T) {
	data := screenTestJPEG(t, 2, 2)
	for _, tc := range []struct {
		name, format  string
		width, height int32
	}{
		{"mismatch", "jpeg", 1, 1},
		{"zero", "jpeg", 0, 2},
		{"oversized declaration", "jpeg", 8193, 2},
		{"wrong format", "png", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeScreenImage(&protocol.ScreenFrame{Width: tc.width, Height: tc.height, Format: tc.format, Data: data})
			if err == nil {
				t.Fatal("accepted untrusted frame metadata")
			}
		})
	}
}

func TestDecodeScreenImageChecksJPEGHeaderBeforeDecode(t *testing.T) {
	data := screenTestJPEG(t, 2, 2)
	marker := bytes.Index(data, []byte{0xff, 0xc0})
	if marker < 0 {
		t.Fatal("JPEG SOF marker missing")
	}
	for _, dims := range []struct {
		name          string
		width, height uint16
	}{
		{"axis", 65535, 2}, {"pixels", 8192, 8192}, {"bomb", 65535, 65535},
	} {
		t.Run(dims.name, func(t *testing.T) {
			bomb := append([]byte(nil), data...)
			binary.BigEndian.PutUint16(bomb[marker+5:marker+7], dims.height)
			binary.BigEndian.PutUint16(bomb[marker+7:marker+9], dims.width)
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(bomb))
			if err != nil || cfg.Width != int(dims.width) || cfg.Height != int(dims.height) {
				t.Fatalf("invalid fixture: %#v %v", cfg, err)
			}
			// Only call the bounded decoder: never full-decode this hostile fixture.
			if _, err := decodeScreenImage(&protocol.ScreenFrame{Width: int32(dims.width), Height: int32(dims.height), Format: "jpeg", Data: bomb}); err == nil || err.Error() != "invalid screen image metadata" {
				t.Fatalf("oversized JPEG did not hit pre-decode metadata guard: %v", err)
			}
			if _, err := decodeScreenImage(&protocol.ScreenFrame{Width: 2, Height: 2, Format: "jpeg", Data: bomb}); err == nil || err.Error() != "screen JPEG dimensions do not match frame metadata" {
				t.Fatalf("lying JPEG did not hit pre-decode header guard: %v", err)
			}
		})
	}
}

func TestDecodeScreenImageValidJPEG(t *testing.T) {
	data := screenTestJPEG(t, 2, 2)
	img, err := decodeScreenImage(&protocol.ScreenFrame{Width: 2, Height: 2, Format: "jpeg", Data: data})
	if err != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("valid JPEG rejected: %v", err)
	}
}
