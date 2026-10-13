package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func box(typ string, body ...[]byte) []byte {
	b := []byte{0, 0, 0, 0}
	b = append(b, typ...)
	for _, p := range body {
		b = append(b, p...)
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	return b
}

// mdhd/mvhd version 0: version+flags, created, modified, timescale, duration.
func durationBox(typ string, scale, dur uint32) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint32(b[12:], scale)
	binary.BigEndian.PutUint32(b[16:], dur)
	return box(typ, b)
}

func hdlr(kind string) []byte {
	b := make([]byte, 24)
	copy(b[8:], kind)
	return box("hdlr", b)
}

func TestRead(t *testing.T) {
	video := box("trak", box("edts"), box("mdia", durationBox("mdhd", 600, 3000), hdlr("vide")))
	audio := box("trak", box("mdia", durationBox("mdhd", 48000, 240000), hdlr("soun")))
	file := bytes.Join([][]byte{
		box("ftyp", []byte("mp42")),
		box("moov", durationBox("mvhd", 1000, 5000), video, audio),
		box("mdat", make([]byte, 100)),
	}, nil)
	f, err := Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if !f.FastStart() || len(f.Top) != 3 {
		t.Errorf("top %v, fast start %v", f.Top, f.FastStart())
	}
	if f.Duration != 5 {
		t.Errorf("duration %v", f.Duration)
	}
	v, ok := f.Track("vide")
	if !ok || v.Duration != 5 || !v.Edits {
		t.Errorf("video %+v", v)
	}
	a, ok := f.Track("soun")
	if !ok || a.Duration != 5 || a.Edits {
		t.Errorf("audio %+v", a)
	}
}

func TestMdatFirstIsNotFastStart(t *testing.T) {
	file := bytes.Join([][]byte{box("ftyp"), box("mdat", make([]byte, 8)), box("moov")}, nil)
	f, err := Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if f.FastStart() {
		t.Error("mdat before moov read as fast start")
	}
}

func TestTruncated(t *testing.T) {
	file := box("ftyp", []byte("mp42"))
	file = append(file, 0, 0, 1, 0, 'm', 'd', 'a', 't') // claims 256 bytes, has none
	if _, err := Read(bytes.NewReader(file), int64(len(file))); err == nil {
		t.Error("read a truncated file without an error")
	}
}
