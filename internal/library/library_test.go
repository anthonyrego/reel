package library

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func box(typ string, body ...[]byte) []byte {
	b := append([]byte{0, 0, 0, 0}, typ...)
	for _, p := range body {
		b = append(b, p...)
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	return b
}

// mp4File is a tiny MP4 of secs seconds, its moov first unless last.
func mp4File(secs uint32, moovLast bool) []byte {
	mvhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], secs*1000)
	moov := box("moov", box("mvhd", mvhd))
	mdat := box("mdat", []byte("xxxx"))
	if moovLast {
		return bytes.Join([][]byte{box("ftyp"), mdat, moov}, nil)
	}
	return bytes.Join([][]byte{box("ftyp"), moov, mdat}, nil)
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte, age time.Duration) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		at := time.Now().Add(-age)
		os.Chtimes(p, at, at)
	}
	write("Recording old.mp4", mp4File(62, false), 2*time.Hour)
	write("Recording new.mp4", mp4File(5, false), time.Minute)
	write("Recording unfinished.mp4", mp4File(9, true), 0)
	write(".hidden.mp4", mp4File(1, false), 0)
	write("notes.txt", []byte("hi"), 0)
	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Recording new" || got[1].Duration != 62*time.Second {
		t.Fatalf("%+v", got)
	}
	if none, err := List(filepath.Join(dir, "missing")); err != nil || none != nil {
		t.Errorf("a missing dir: %v %v", none, err)
	}
}

func TestNewPath(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 13, 15, 4, 5, 0, time.Local)
	p := NewPath(dir, at)
	if filepath.Base(p) != "Recording 2026-10-13 at 15.04.05.mp4" {
		t.Errorf("%s", p)
	}
	os.WriteFile(p, nil, 0o644)
	if p2 := NewPath(dir, at); filepath.Base(p2) != "Recording 2026-10-13 at 15.04.05 2.mp4" {
		t.Errorf("%s", p2)
	}
}

func TestTrimmedPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Recording 2026-10-13 at 15.04.05.mp4")
	p := TrimmedPath(src)
	if filepath.Base(p) != "Recording 2026-10-13 at 15.04.05 trimmed.mp4" {
		t.Errorf("%s", p)
	}
	os.WriteFile(p, nil, 0o644)
	p2 := TrimmedPath(src)
	if filepath.Base(p2) != "Recording 2026-10-13 at 15.04.05 trimmed 2.mp4" {
		t.Errorf("%s", p2)
	}
	os.WriteFile(p2, nil, 0o644)
	// Trimming a trimmed copy numbers it.
	for _, from := range []string{p, p2} {
		if got := filepath.Base(TrimmedPath(from)); got != "Recording 2026-10-13 at 15.04.05 trimmed 3.mp4" {
			t.Errorf("from %s: %s", filepath.Base(from), got)
		}
	}
	if got := filepath.Base(TrimmedPath(filepath.Join(dir, "untrimmed take.mp4"))); got != "untrimmed take trimmed.mp4" {
		t.Errorf("%s", got)
	}
}
