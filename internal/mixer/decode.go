package mixer

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Format describes linear PCM as CoreAudio does (an AudioStreamBasicDescription's
// essentials).
type Format struct {
	Rate        float64
	Channels    int
	Bits        int // 16, 24 or 32
	Float       bool
	Interleaved bool // one buffer of interleaved channels; else one buffer per channel
	BigEndian   bool
}

// Decode converts buffers in format f into planar float32, one slice per
// channel, keeping at most keep channels from the first (a microphone
// array's first channel is the usable one: keep 1). dst is reused when it
// has room.
func Decode(f Format, bufs [][]byte, keep int, dst [][]float32) ([][]float32, error) {
	bytesPer := f.Bits / 8
	if bytesPer < 2 || bytesPer > 4 || (f.Float && f.Bits != 32) || f.Channels < 1 {
		return nil, fmt.Errorf("mixer: can't read %d-bit %s PCM with %d channels", f.Bits, kind(f), f.Channels)
	}
	chans := min(keep, f.Channels)
	if chans < 1 {
		chans = 1
	}
	var frames int
	switch {
	case f.Interleaved:
		if len(bufs) < 1 {
			return nil, fmt.Errorf("mixer: no buffer")
		}
		frames = len(bufs[0]) / (bytesPer * f.Channels)
	default:
		if len(bufs) < chans {
			return nil, fmt.Errorf("mixer: %d buffers for %d channels", len(bufs), f.Channels)
		}
		frames = len(bufs[0]) / bytesPer
	}
	if cap(dst) < chans {
		dst = make([][]float32, chans)
	}
	dst = dst[:chans]
	for c := range chans {
		if cap(dst[c]) < frames {
			dst[c] = make([]float32, frames)
		}
		dst[c] = dst[c][:frames]
	}
	var order binary.ByteOrder = binary.LittleEndian
	if f.BigEndian {
		order = binary.BigEndian
	}
	read := sampleReader(f, order)
	for c := range chans {
		out := dst[c]
		if f.Interleaved {
			stride := bytesPer * f.Channels
			b := bufs[0]
			for i := range frames {
				out[i] = read(b[i*stride+c*bytesPer:])
			}
			continue
		}
		b := bufs[c]
		for i := range frames {
			out[i] = read(b[i*bytesPer:])
		}
	}
	return dst, nil
}

// sampleReader reads one sample as a float in −1…1.
func sampleReader(f Format, order binary.ByteOrder) func([]byte) float32 {
	switch {
	case f.Float:
		return func(b []byte) float32 { return math.Float32frombits(order.Uint32(b)) }
	case f.Bits == 16:
		return func(b []byte) float32 { return float32(int16(order.Uint16(b))) / 32768 }
	case f.Bits == 24:
		return func(b []byte) float32 {
			var v int32
			if order == binary.BigEndian {
				v = int32(b[0])<<24 | int32(b[1])<<16 | int32(b[2])<<8
			} else {
				v = int32(b[2])<<24 | int32(b[1])<<16 | int32(b[0])<<8
			}
			return float32(v) / 2147483648
		}
	default:
		return func(b []byte) float32 { return float32(int32(order.Uint32(b))) / 2147483648 }
	}
}

func kind(f Format) string {
	if f.Float {
		return "float"
	}
	return "integer"
}
