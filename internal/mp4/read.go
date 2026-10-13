// Package mp4 reads the structure of an MP4 file (the order of its
// top-level boxes and what each track is, so a test can check what was
// written without a player) and moves its index to the front (FastStart).
// Pure.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// File is what Read found.
type File struct {
	Top    []string // top-level box types in order ("ftyp", "moov", "mdat", …)
	Tracks []Track
	// Duration is the movie's (mvhd), in seconds.
	Duration float64
}

// Track is one trak box.
type Track struct {
	Handler  string  // "vide", "soun"
	Duration float64 // the media's own (mdhd), seconds
	Edits    bool    // has an edit list (edts): players trim or shift it
	Width    float64 // tkhd, for video
	Height   float64
}

// FastStart reports whether moov comes before mdat, so a player (Slack,
// a browser) can start before it has the whole file.
func (f File) FastStart() bool {
	for _, t := range f.Top {
		switch t {
		case "moov":
			return true
		case "mdat":
			return false
		}
	}
	return false
}

// Track returns the first track with the handler ("vide", "soun").
func (f File) Track(handler string) (Track, bool) {
	for _, t := range f.Tracks {
		if t.Handler == handler {
			return t, true
		}
	}
	return Track{}, false
}

// ReadFile reads the MP4 at path.
func ReadFile(path string) (File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return File{}, err
	}
	return Read(fh, st.Size())
}

// maxMoov bounds the moov box Read loads into memory.
const maxMoov = 64 << 20

// Read reads an MP4 of size bytes.
func Read(r io.ReaderAt, size int64) (File, error) {
	var f File
	var off int64
	for off < size {
		typ, hdr, n, err := header(r, off, size)
		if err != nil {
			return f, err
		}
		f.Top = append(f.Top, typ)
		if typ == "moov" {
			if n-hdr > maxMoov {
				return f, fmt.Errorf("moov of %d bytes", n)
			}
			body := make([]byte, n-hdr)
			if _, err := r.ReadAt(body, off+hdr); err != nil && !(errors.Is(err, io.EOF) && len(body) == 0) {
				return f, fmt.Errorf("moov: %w", err)
			}
			if err := f.moov(body); err != nil {
				return f, err
			}
		}
		off += n
	}
	if len(f.Top) == 0 {
		return f, errors.New("no boxes")
	}
	return f, nil
}

// header reads the box at off: its type, header length and total length.
func header(r io.ReaderAt, off, size int64) (string, int64, int64, error) {
	var b [16]byte
	if _, err := r.ReadAt(b[:8], off); err != nil {
		return "", 0, 0, fmt.Errorf("box at %d: %w", off, err)
	}
	n, hdr := int64(binary.BigEndian.Uint32(b[:4])), int64(8)
	typ := string(b[4:8])
	switch n {
	case 0: // to the end of the file
		n = size - off
	case 1: // 64-bit size follows
		if _, err := r.ReadAt(b[8:16], off+8); err != nil {
			return "", 0, 0, fmt.Errorf("box %s at %d: %w", typ, off, err)
		}
		n, hdr = int64(binary.BigEndian.Uint64(b[8:16])), 16
	}
	if n < hdr || off+n > size {
		return "", 0, 0, fmt.Errorf("box %q at %d has a bad size %d (file %d)", typ, off, n, size)
	}
	return typ, hdr, n, nil
}

// children calls fn with each box in b.
func children(b []byte, fn func(typ string, body []byte) error) error {
	for len(b) >= 8 {
		n, hdr := uint64(binary.BigEndian.Uint32(b[:4])), uint64(8)
		typ := string(b[4:8])
		if n == 1 && len(b) >= 16 {
			n, hdr = binary.BigEndian.Uint64(b[8:16]), 16
		} else if n == 0 {
			n = uint64(len(b))
		}
		if n < hdr || n > uint64(len(b)) {
			return fmt.Errorf("box %q has a bad size %d", typ, n)
		}
		if err := fn(typ, b[hdr:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (f *File) moov(b []byte) error {
	return children(b, func(typ string, body []byte) error {
		switch typ {
		case "mvhd":
			f.Duration = fullDuration(body)
		case "trak":
			t, err := trak(body)
			if err != nil {
				return err
			}
			f.Tracks = append(f.Tracks, t)
		}
		return nil
	})
}

func trak(b []byte) (Track, error) {
	var t Track
	err := children(b, func(typ string, body []byte) error {
		switch typ {
		case "edts":
			t.Edits = true
		case "tkhd":
			// version, flags, then times and ids; width and height are the
			// last 8 bytes, 16.16 fixed point.
			if len(body) >= 8 {
				t.Width = float64(binary.BigEndian.Uint32(body[len(body)-8:])) / 65536
				t.Height = float64(binary.BigEndian.Uint32(body[len(body)-4:])) / 65536
			}
		case "mdia":
			return children(body, func(typ string, body []byte) error {
				switch typ {
				case "mdhd":
					t.Duration = fullDuration(body)
				case "hdlr":
					if len(body) >= 12 {
						t.Handler = string(body[8:12])
					}
				}
				return nil
			})
		}
		return nil
	})
	return t, err
}

// fullDuration reads timescale and duration from an mvhd or mdhd body,
// in seconds.
func fullDuration(b []byte) float64 {
	if len(b) < 4 {
		return 0
	}
	if b[0] == 1 { // version 1: 64-bit times
		if len(b) < 32 {
			return 0
		}
		scale := binary.BigEndian.Uint32(b[20:24])
		dur := binary.BigEndian.Uint64(b[24:32])
		if scale == 0 {
			return 0
		}
		return float64(dur) / float64(scale)
	}
	if len(b) < 20 {
		return 0
	}
	scale := binary.BigEndian.Uint32(b[12:16])
	dur := binary.BigEndian.Uint32(b[16:20])
	if scale == 0 {
		return 0
	}
	return float64(dur) / float64(scale)
}
