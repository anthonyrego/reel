package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// AlignEdits makes each video track's edit list start on a frame.
//
// A trim copied without re-encoding starts at the keyframe before the cut,
// and its edit list skips to the cut, which usually falls while an earlier
// frame is on screen. That's correct, but ffmpeg (and the players built on
// it: browsers, Slack) starts the picture at the first frame after the
// cut, so every frame plays early by the gap. In a recording of a still
// screen, which only writes frames when something changes, the gap can be
// seconds. Here the frame on screen at the cut is retimed to start exactly
// at the cut (the frame before it, or the media's start, takes up the
// difference). Every player then shows the same thing, and nothing after
// the cut moves.
//
// Only a moov-first file is changed, and only a video track with one edit
// and no composition offsets (reel writes no B-frames). The file is
// rewritten through a temporary file and renamed. AlignEdits reports how
// far the frame moved, in seconds (0: there was nothing to do).
func AlignEdits(path string) (float64, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	boxes, err := topLevel(in, st.Size())
	if err != nil {
		return 0, err
	}
	at := -1
	for i, b := range boxes {
		if b.typ == "mdat" && at < 0 {
			return 0, errors.New("align: the media data comes before the moov")
		}
		if b.typ == "moov" {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, errors.New("align: no moov")
	}
	mv := boxes[at]
	if mv.size > maxMoov {
		return 0, fmt.Errorf("align: moov of %d bytes", mv.size)
	}
	moov := make([]byte, mv.size)
	if _, err := in.ReadAt(moov, mv.off); err != nil {
		return 0, fmt.Errorf("align: read moov: %w", err)
	}
	moved := 0.0
	var body []byte
	err = children(moov[mv.hdr:], func(typ string, b []byte) error {
		if typ == "trak" {
			nb, m, err := alignTrak(b)
			if err != nil {
				return err
			}
			b, moved = nb, max(moved, m)
		}
		body = appendBox(body, typ, b)
		return nil
	})
	if err != nil || moved == 0 {
		return 0, err
	}
	out := appendBox(nil, "moov", body)
	// The media data after the moov moves by however much the moov grew.
	if delta := int64(len(out)) - mv.size; delta != 0 {
		if err := shiftOffsets(out[8:], mv.off+mv.size, math.MaxInt64, delta); err != nil {
			return 0, err
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".align-*")
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	for i, b := range boxes {
		if i == at {
			_, err = tmp.Write(out)
		} else {
			_, err = io.Copy(tmp, io.NewSectionReader(in, b.off, b.size))
		}
		if err != nil {
			return 0, err
		}
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, err
	}
	ok = true
	return moved, nil
}

// alignTrak aligns one trak's edit (see AlignEdits). It returns the trak's
// new body and how far the frame moved, in seconds. A trak it leaves alone
// comes back as it was, with 0.
func alignTrak(b []byte) ([]byte, float64, error) {
	var handler string
	var elst, stts, mdhd []byte
	ctts := false
	if _, err := rewrite(b, func(typ string, body []byte) []byte {
		switch typ {
		case "hdlr":
			if len(body) >= 12 {
				handler = string(body[8:12])
			}
		case "elst":
			elst = body
		case "stts":
			stts = body
		case "mdhd":
			mdhd = body
		case "ctts":
			ctts = true
		}
		return body
	}); err != nil {
		return nil, 0, err
	}
	scale, _ := fullTimes(mdhd)
	if handler != "vide" || ctts || scale == 0 || elst == nil || stts == nil {
		return b, 0, nil
	}
	start, ok := oneEdit(elst)
	deltas, err := sampleDeltas(stts)
	if !ok || err != nil || start <= 0 || len(deltas) < 2 {
		return b, 0, err
	}
	// k is the frame on screen at the edit's start.
	k, dts := 0, int64(0)
	for k+1 < len(deltas) && dts+int64(deltas[k]) <= start {
		dts += int64(deltas[k])
		k++
	}
	gap := start - dts
	if gap == 0 || int64(deltas[k]) <= gap {
		return b, 0, nil
	}
	newStart := start
	if k == 0 {
		// No frame before it: the media starts later instead.
		deltas[0] -= uint32(gap)
		newStart = 0
	} else {
		deltas[k-1] += uint32(gap)
		deltas[k] -= uint32(gap)
	}
	out, err := rewrite(b, func(typ string, body []byte) []byte {
		switch typ {
		case "elst":
			return setEdit(body, newStart)
		case "stts":
			return packDeltas(deltas)
		case "mdhd":
			if k == 0 {
				return shortenMdhd(body, uint64(gap))
			}
		}
		return body
	})
	return out, float64(gap) / float64(scale), err
}

// alignContainers are the boxes alignTrak looks inside.
var alignContainers = map[string]bool{"edts": true, "mdia": true, "minf": true, "stbl": true}

// rewrite rebuilds the boxes in b, looking inside alignContainers. fn gets
// every other box's type and body and returns its new body.
func rewrite(b []byte, fn func(typ string, body []byte) []byte) ([]byte, error) {
	var out []byte
	err := children(b, func(typ string, body []byte) error {
		if alignContainers[typ] {
			nb, err := rewrite(body, fn)
			if err != nil {
				return err
			}
			body = nb
		} else {
			body = fn(typ, body)
		}
		out = appendBox(out, typ, body)
		return nil
	})
	return out, err
}

// appendBox appends a box with a 32-bit size (a moov's boxes are small).
func appendBox(out []byte, typ string, body []byte) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(body)))
	out = append(out, typ...)
	return append(out, body...)
}

// oneEdit reads an elst body with a single edit at normal speed: the
// media time it starts at.
func oneEdit(b []byte) (int64, bool) {
	if len(b) < 8 || binary.BigEndian.Uint32(b[4:8]) != 1 {
		return 0, false
	}
	if b[0] == 1 {
		if len(b) < 8+20 || binary.BigEndian.Uint32(b[24:28]) != 1<<16 {
			return 0, false
		}
		return int64(binary.BigEndian.Uint64(b[16:24])), true
	}
	if len(b) < 8+12 || binary.BigEndian.Uint32(b[16:20]) != 1<<16 {
		return 0, false
	}
	return int64(int32(binary.BigEndian.Uint32(b[12:16]))), true
}

// setEdit is a copy of a one-edit elst body starting at media time at.
func setEdit(b []byte, at int64) []byte {
	b = append([]byte(nil), b...)
	if b[0] == 1 {
		binary.BigEndian.PutUint64(b[16:24], uint64(at))
	} else {
		binary.BigEndian.PutUint32(b[12:16], uint32(int32(at)))
	}
	return b
}

// sampleDeltas expands an stts body into each sample's duration.
func sampleDeltas(b []byte) ([]uint32, error) {
	if len(b) < 8 {
		return nil, errors.New("align: short stts")
	}
	runs := int(binary.BigEndian.Uint32(b[4:8]))
	if len(b) < 8+8*runs {
		return nil, fmt.Errorf("align: stts claims %d runs", runs)
	}
	var out []uint32
	for i := range runs {
		n := binary.BigEndian.Uint32(b[8+8*i:])
		d := binary.BigEndian.Uint32(b[12+8*i:])
		if len(out)+int(n) > 1<<24 {
			return nil, errors.New("align: too many samples")
		}
		for range n {
			out = append(out, d)
		}
	}
	return out, nil
}

// packDeltas is an stts body for each sample's duration.
func packDeltas(deltas []uint32) []byte {
	var runs []byte
	count := 0
	for i := 0; i < len(deltas); {
		j := i
		for j < len(deltas) && deltas[j] == deltas[i] {
			j++
		}
		runs = binary.BigEndian.AppendUint32(runs, uint32(j-i))
		runs = binary.BigEndian.AppendUint32(runs, deltas[i])
		count++
		i = j
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 4), uint32(count))
	return append(out, runs...)
}

// shortenMdhd is a copy of an mdhd body with its duration less by.
func shortenMdhd(b []byte, by uint64) []byte {
	b = append([]byte(nil), b...)
	if b[0] == 1 && len(b) >= 32 {
		binary.BigEndian.PutUint64(b[24:32], binary.BigEndian.Uint64(b[24:32])-by)
	} else if len(b) >= 20 {
		binary.BigEndian.PutUint32(b[16:20], binary.BigEndian.Uint32(b[16:20])-uint32(by))
	}
	return b
}
