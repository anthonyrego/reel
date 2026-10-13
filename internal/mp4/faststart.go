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

// FastStart moves the file's moov box in front of its media data, so a
// player can start before it has the whole file (Slack, a browser). The
// recording writes movie fragments as it goes (so a crash loses at most
// one fragment), and AVAssetWriter puts the final moov at the end; this is
// the same rewrite its network-use option would do.
//
// The new file is ftyp, moov, then every other box in its old order. Media
// data that was before the moov moves later by the moov's size, so every
// chunk offset (stco, co64) pointing there is moved too. It writes a
// temporary file beside path and renames it over path; on an error path is
// left as it was. A file that is already fast-start is left alone.
func FastStart(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	boxes, err := topLevel(in, st.Size())
	if err != nil {
		return err
	}
	moovAt, mdatAt := -1, -1
	for i, b := range boxes {
		switch b.typ {
		case "moov":
			if moovAt < 0 {
				moovAt = i
			}
		case "mdat":
			if mdatAt < 0 {
				mdatAt = i
			}
		}
	}
	if moovAt < 0 {
		return errors.New("faststart: no moov (the recording didn't finish)")
	}
	if mdatAt < 0 || moovAt < mdatAt {
		return nil // nothing to move
	}
	mv := boxes[moovAt]
	moov := make([]byte, mv.size)
	if _, err := in.ReadAt(moov, mv.off); err != nil {
		return fmt.Errorf("faststart: read moov: %w", err)
	}
	// Everything before the moov's old place moves down by its size,
	// except an ftyp at the very start, which stays first.
	shiftFrom := int64(0)
	if boxes[0].typ == "ftyp" {
		shiftFrom = boxes[0].size
	}
	if err := shiftOffsets(moov[mv.hdr:], shiftFrom, mv.off, mv.size); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".faststart-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	copyBox := func(b topBox) error {
		_, err := io.Copy(tmp, io.NewSectionReader(in, b.off, b.size))
		return err
	}
	start := 0
	if boxes[0].typ == "ftyp" {
		if err := copyBox(boxes[0]); err != nil {
			return err
		}
		start = 1
	}
	if _, err := tmp.Write(moov); err != nil {
		return err
	}
	for i := start; i < len(boxes); i++ {
		if i == moovAt {
			continue
		}
		if err := copyBox(boxes[i]); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	ok = true
	return nil
}

type topBox struct {
	typ       string
	off, size int64
	hdr       int64
}

func topLevel(r io.ReaderAt, size int64) ([]topBox, error) {
	var out []topBox
	for off := int64(0); off < size; {
		typ, hdr, n, err := header(r, off, size)
		if err != nil {
			return nil, err
		}
		out = append(out, topBox{typ, off, n, hdr})
		off += n
	}
	if len(out) == 0 {
		return nil, errors.New("faststart: empty file")
	}
	return out, nil
}

// containers are the boxes on the way from moov to the chunk offsets.
var containers = map[string]bool{"trak": true, "mdia": true, "minf": true, "stbl": true}

// shiftOffsets adds delta to every chunk offset in a moov body that points
// at or after from and before the moov's old place (before), in place.
func shiftOffsets(b []byte, from, before, delta int64) error {
	for len(b) >= 8 {
		n, hdr := int64(binary.BigEndian.Uint32(b[:4])), int64(8)
		typ := string(b[4:8])
		if n == 1 && len(b) >= 16 {
			n, hdr = int64(binary.BigEndian.Uint64(b[8:16])), 16
		} else if n == 0 {
			n = int64(len(b))
		}
		if n < hdr || n > int64(len(b)) {
			return fmt.Errorf("faststart: box %q has a bad size %d", typ, n)
		}
		body := b[hdr:n]
		switch {
		case containers[typ]:
			if err := shiftOffsets(body, from, before, delta); err != nil {
				return err
			}
		case typ == "stco" || typ == "co64":
			if err := shiftTable(typ, body, from, before, delta); err != nil {
				return err
			}
		}
		b = b[n:]
	}
	return nil
}

// shiftTable moves the entries of an stco (32-bit) or co64 table: version
// and flags, a count, then the offsets.
func shiftTable(typ string, b []byte, from, before, delta int64) error {
	if len(b) < 8 {
		return fmt.Errorf("faststart: short %s", typ)
	}
	count := int(binary.BigEndian.Uint32(b[4:8]))
	width := 4
	if typ == "co64" {
		width = 8
	}
	if len(b) < 8+count*width {
		return fmt.Errorf("faststart: %s claims %d entries", typ, count)
	}
	for i := range count {
		p := b[8+i*width : 8+(i+1)*width]
		var v int64
		if width == 4 {
			v = int64(binary.BigEndian.Uint32(p))
		} else {
			v = int64(binary.BigEndian.Uint64(p))
		}
		if v < from || v >= before {
			continue
		}
		v += delta
		if width == 4 {
			if v > math.MaxUint32 {
				return errors.New("faststart: a chunk offset passes 4 GB in a 32-bit table")
			}
			binary.BigEndian.PutUint32(p, uint32(v))
		} else {
			binary.BigEndian.PutUint64(p, uint64(v))
		}
	}
	return nil
}
