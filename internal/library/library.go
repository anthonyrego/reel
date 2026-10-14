// Package library lists the recordings: the MP4s in the recordings
// directory, newest first, with how long each is (read from its moov) and
// how big. It also names the next one. Pure.
package library

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anthonyrego/reel/internal/mp4"
)

// Recording is one file.
type Recording struct {
	Path     string
	Name     string // the file name without .mp4
	Duration time.Duration
	Size     int64
	When     time.Time // when it was made
}

// List reads dir's recordings, newest first. Files that aren't finished
// MP4s (a recording still being written, a crash's leftovers) are left
// out; dot files too.
func List(dir string) ([]Recording, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Recording
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".mp4") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		f, err := mp4.ReadFile(path)
		if err != nil || !f.FastStart() {
			continue // unfinished: reel moves the index to the front last
		}
		out = append(out, Recording{
			Path:     path,
			Name:     strings.TrimSuffix(name, filepath.Ext(name)),
			Duration: time.Duration(f.Duration * float64(time.Second)),
			Size:     info.Size(),
			When:     info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out, nil
}

// NewPath is a path for a recording started at t in dir: "Recording
// 2026-10-13 at 15.04.05.mp4", with a number added if that's taken.
func NewPath(dir string, t time.Time) string {
	base := "Recording " + t.Format("2006-01-02 at 15.04.05")
	path := filepath.Join(dir, base+".mp4")
	for i := 2; exists(path); i++ {
		path = filepath.Join(dir, base+" "+itoa(i)+".mp4")
	}
	return path
}

// TrimmedPath is a path for a trimmed copy of the recording at src, next
// to it: "Recording … trimmed.mp4", with a number added if that's taken.
// A copy of a trimmed copy is numbered, not trimmed twice.
func TrimmedPath(src string) string {
	dir, name := filepath.Split(src)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.LastIndex(base, " trimmed"); i >= 0 && allDigits(strings.TrimPrefix(base[i+len(" trimmed"):], " ")) {
		base = base[:i]
	}
	base += " trimmed"
	path := filepath.Join(dir, base+".mp4")
	for i := 2; exists(path); i++ {
		path = filepath.Join(dir, base+" "+itoa(i)+".mp4")
	}
	return path
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
