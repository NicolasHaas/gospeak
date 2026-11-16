package client

import (
	"image"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/screenshare"
)

func TestScreenEncoderGeometryRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		width, height, maxWidth, wantWidth, wantHeight int
	}{
		{"landscape", 1920, 1080, 0, 1280, 720},
		{"portrait", 1080, 1920, 0, 720, 1280},
		{"ultrawide", 2560, 720, 0, 1280, 360},
		{"odd", 641, 479, 0, 641, 479},
		{"tiny", 1, 1, 0, 1, 1},
		{"cap", 1280, 1280, 0, 1280, 1280},
		{"tall", 1, 2561, 0, 1, 1280},
		{"thin", 2561, 1, 0, 1280, 1},
		{"width override", 1000, 500, 320, 320, 160},
		{"oversized override", 1281, 1, 4096, 1280, 1},
		{"width alone exceeds pixels", 640, 2561, 0, 319, 1280},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, width, height, err := screenshare.EncodeJPEG(image.NewGray(image.Rect(0, 0, tc.width, tc.height)), tc.maxWidth, 0)
			if err != nil {
				t.Fatal(err)
			}
			if int(width) != tc.wantWidth || int(height) != tc.wantHeight {
				t.Fatalf("encoded %dx%d, want %dx%d", width, height, tc.wantWidth, tc.wantHeight)
			}
			wire, err := protocol.MarshalScreenFrame(&protocol.ScreenFrame{Width: width, Height: height, Format: "jpeg", Data: data})
			if err != nil {
				t.Fatal(err)
			}
			frame, err := protocol.UnmarshalScreenFrame(wire)
			if err != nil {
				t.Fatal(err)
			}
			img, err := decodeScreenImage(frame)
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != int(width) || img.Bounds().Dy() != int(height) {
				t.Fatalf("decoded bounds %v disagree with encoded metadata", img.Bounds())
			}
		})
	}
}

func TestScreenEncoderRejectsEmptyImage(t *testing.T) {
	if _, _, _, err := screenshare.EncodeJPEG(image.NewGray(image.Rectangle{}), 0, 0); err == nil {
		t.Fatal("encoded empty image")
	}
}
