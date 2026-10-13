package mp4

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// table is an stco or co64 body: version and flags, count, entries.
func table(typ string, offs ...uint64) []byte {
	b := append(u32(0), u32(uint32(len(offs)))...)
	for _, o := range offs {
		if typ == "stco" {
			b = append(b, u32(uint32(o))...)
		} else {
			b = append(b, u64(o)...)
		}
	}
	return box(typ, b)
}

// recording is ftyp, mdat (two chunks), free, moov at the end: what
// AVAssetWriter leaves after fragments. Chunk A is in one track's stco,
// chunk B in another's co64.
func recording() (file []byte, chunks map[string]int64) {
	ftyp := box("ftyp", []byte("isom"))
	mdat := box("mdat", []byte("AAAAxxBBBB"))
	a := int64(len(ftyp) + 8)
	b := a + 6
	moov := box("moov", durationBox("mvhd", 1000, 1000),
		box("trak", box("mdia", hdlr("vide"), box("minf", box("stbl", table("stco", uint64(a)))))),
		box("trak", box("mdia", hdlr("soun"), box("minf", box("stbl", table("co64", uint64(b)))))),
	)
	file = bytes.Join([][]byte{ftyp, mdat, box("free", make([]byte, 4)), moov}, nil)
	return file, map[string]int64{"AAAA": a, "BBBB": b}
}

// offsets reads every stco/co64 entry of the file's moov, in order.
func offsets(t *testing.T, file []byte) []int64 {
	t.Helper()
	boxes, err := topLevel(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	var walk func(b []byte)
	walk = func(b []byte) {
		children(b, func(typ string, body []byte) error {
			switch {
			case containers[typ]:
				walk(body)
			case typ == "stco":
				for i := 0; i < int(binary.BigEndian.Uint32(body[4:8])); i++ {
					out = append(out, int64(binary.BigEndian.Uint32(body[8+4*i:])))
				}
			case typ == "co64":
				for i := 0; i < int(binary.BigEndian.Uint32(body[4:8])); i++ {
					out = append(out, int64(binary.BigEndian.Uint64(body[8+8*i:])))
				}
			}
			return nil
		})
	}
	for _, b := range boxes {
		if b.typ == "moov" {
			walk(file[b.off+b.hdr : b.off+b.size])
		}
	}
	return out
}

func TestFastStart(t *testing.T) {
	file, chunks := recording()
	path := filepath.Join(t.TempDir(), "rec.mp4")
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FastStart(path); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(file) {
		t.Fatalf("size %d, was %d", len(out), len(file))
	}
	f, err := Read(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	if !f.FastStart() || f.Top[0] != "ftyp" || len(f.Tracks) != 2 {
		t.Fatalf("top %v, %d tracks", f.Top, len(f.Tracks))
	}
	offs := offsets(t, out)
	want := []string{"AAAA", "BBBB"}
	if len(offs) != len(want) {
		t.Fatalf("offsets %v", offs)
	}
	for i, o := range offs {
		if got := string(out[o : o+4]); got != want[i] {
			t.Errorf("chunk %d at %d (was %d) reads %q, want %q", i, o, chunks[want[i]], got, want[i])
		}
	}
	// Already fast-start: left alone.
	before, _ := os.ReadFile(path)
	if err := FastStart(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("a fast-start file was rewritten")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*faststart*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestFastStartNeedsMoov(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cut.mp4")
	os.WriteFile(path, bytes.Join([][]byte{box("ftyp"), box("mdat", []byte("x"))}, nil), 0o644)
	if err := FastStart(path); err == nil {
		t.Error("no error for a file without a moov")
	}
}
