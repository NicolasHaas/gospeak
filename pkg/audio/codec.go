package audio

import (
	"fmt"

	"github.com/hraban/opus"
)

const (
	opusSampleRate = 48000
	opusChannels   = 1
	opusBitrate    = 64000 // 64 kbps - good quality for voice
	opusFrameSize  = 960   // 20ms at 48kHz
)

// Encoder wraps an Opus encoder.
type Encoder struct {
	enc *opus.Encoder
	buf []byte // reusable output buffer
}

// NewEncoder creates a new Opus encoder optimized for voice.
func NewEncoder() (*Encoder, error) {
	enc, err := opus.NewEncoder(opusSampleRate, opusChannels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("audio: new encoder: %w", err)
	}

	_ = enc.SetBitrate(opusBitrate)
	_ = enc.SetInBandFEC(true)    // Forward error correction
	_ = enc.SetPacketLossPerc(10) // Optimize FEC for up to 10% packet loss
	_ = enc.SetDTX(true)          // Discontinuous transmission (saves bandwidth on silence)

	return &Encoder{
		enc: enc,
		buf: make([]byte, 1024), // max Opus frame size
	}, nil
}

// Encode encodes a PCM frame to Opus. Returns the encoded bytes.
func (e *Encoder) Encode(pcm []int16) ([]byte, error) {
	n, err := e.enc.Encode(pcm, e.buf)
	if err != nil {
		return nil, fmt.Errorf("audio: encode: %w", err)
	}
	out := make([]byte, n)
	copy(out, e.buf[:n])
	return out, nil
}

// Decoder wraps an Opus decoder.
type Decoder struct {
	dec *opus.Decoder
}

// NewDecoder creates a new Opus decoder.
func NewDecoder() (*Decoder, error) {
	dec, err := opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		return nil, fmt.Errorf("audio: new decoder: %w", err)
	}
	return &Decoder{dec: dec}, nil
}

// Decode decodes an Opus frame to PCM.
func (d *Decoder) Decode(opusData []byte) ([]int16, error) {
	pcm := make([]int16, opusFrameSize)
	n, err := d.dec.Decode(opusData, pcm)
	if err != nil {
		return nil, fmt.Errorf("audio: decode: %w", err)
	}
	return pcm[:n], nil
}

// DecodePLC performs Packet Loss Concealment (generates audio to fill a gap).
func (d *Decoder) DecodePLC() ([]int16, error) {
	pcm := make([]int16, opusFrameSize)
	n, err := d.dec.Decode(nil, pcm)
	if err != nil {
		return nil, fmt.Errorf("audio: decode plc: %w", err)
	}
	return pcm[:n], nil
}
