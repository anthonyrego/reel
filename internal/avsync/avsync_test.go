package avsync

import (
	"math"
	"testing"
)

// A flash every 0.5 s, 0.1 s long, filmed at 30 fps starting at 0.01 s.
func flashes() []Point {
	var ps []Point
	for i := 0; i < 90; i++ {
		t := 0.01 + float64(i)/30
		v := 16.0
		if math.Mod(t, 0.5) < 0.1 {
			v = 235
		}
		ps = append(ps, Point{t, v})
	}
	return ps
}

func TestOnsets(t *testing.T) {
	ps := flashes()
	got := Onsets(ps, Midway(ps), 0.25)
	if len(got) != 6 {
		t.Fatalf("got %d onsets: %v", len(got), got)
	}
	for i, at := range got {
		if want := float64(i) * 0.5; at < want || at > want+1.0/30+0.011 {
			t.Errorf("onset %d at %.3f, want just after %.3f", i, at, want)
		}
	}
}

func TestAudioOnsetsAndPair(t *testing.T) {
	const rate = 48000.0
	pcm := make([]float32, int(3*rate))
	for _, at := range []float64{0.04, 0.54, 1.04, 1.54, 2.04, 2.54} {
		for i := 0; i < 960; i++ {
			pcm[int(at*rate)+i] = float32(0.1 * math.Sin(2*math.Pi*1000*float64(i)/rate))
		}
	}
	audio := AudioOnsets(pcm, rate, 0.03, 0.2)
	if len(audio) != 6 {
		t.Fatalf("got %d clicks: %v", len(audio), audio)
	}
	video := []float64{0.01, 0.51, 1.01, 1.51, 2.01, 2.51}
	s := Summarize(Pair(video, audio, 0.2))
	if s.N != 6 || math.Abs(s.Median-0.03) > 0.001 || s.P95-s.P5 > 0.001 {
		t.Errorf("summary %+v", s)
	}
}

func TestPairLeavesOutFarOnes(t *testing.T) {
	if got := Pair([]float64{1, 2}, []float64{1.05, 3}, 0.2); len(got) != 1 {
		t.Errorf("pairs %v", got)
	}
}
