// Package avsync measures what a self-test recorded: when the beacon's
// flashes appear in the video, when its clicks are heard in the audio, and
// how far apart the two are. Pure.
package avsync

import (
	"math"
	"slices"
)

// Point is a value at a time (seconds): a video frame's brightness at its
// presentation time.
type Point struct{ T, V float64 }

// Onsets are the times the series rises through threshold, at most one
// every minGap seconds. A video frame is shown from its time on, so an
// onset is the first frame above the threshold.
func Onsets(ps []Point, threshold, minGap float64) []float64 {
	var out []float64
	above := false
	last := math.Inf(-1)
	for _, p := range ps {
		switch {
		case p.V >= threshold && !above:
			above = true
			if p.T-last >= minGap {
				out = append(out, p.T)
				last = p.T
			}
		case p.V < threshold:
			above = false
		}
	}
	return out
}

// Midway is the threshold halfway between the series' least and greatest
// values.
func Midway(ps []Point) float64 {
	if len(ps) == 0 {
		return 0
	}
	lo, hi := ps[0].V, ps[0].V
	for _, p := range ps {
		lo, hi = math.Min(lo, p.V), math.Max(hi, p.V)
	}
	return (lo + hi) / 2
}

// AudioOnsets are the times (seconds) mono PCM at rate first rises above
// threshold after at least minGap of staying under it: a click's start.
func AudioOnsets(pcm []float32, rate, threshold, minGap float64) []float64 {
	var out []float64
	quiet := int(minGap * rate)
	since := quiet // samples since the last loud one
	for i, v := range pcm {
		if math.Abs(float64(v)) >= threshold {
			if since >= quiet {
				out = append(out, float64(i)/rate)
			}
			since = 0
			continue
		}
		since++
	}
	return out
}

// Pair matches each video onset with the nearest audio onset within
// maxDist and returns audio − video for each pair: positive when the
// sound comes after the picture.
func Pair(video, audio []float64, maxDist float64) []float64 {
	var out []float64
	for _, v := range video {
		best := math.Inf(1)
		for _, a := range audio {
			if d := a - v; math.Abs(d) < math.Abs(best) {
				best = d
			}
		}
		if math.Abs(best) <= maxDist {
			out = append(out, best)
		}
	}
	return out
}

// Summary describes a set of offsets (seconds).
type Summary struct {
	N               int
	Median, P5, P95 float64
	Min, Max        float64
}

// Summarize sorts a copy of xs and reads its quantiles.
func Summarize(xs []float64) Summary {
	if len(xs) == 0 {
		return Summary{}
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	q := func(f float64) float64 { return s[min(len(s)-1, int(f*float64(len(s)-1)+0.5))] }
	return Summary{N: len(s), Median: q(0.5), P5: q(0.05), P95: q(0.95), Min: s[0], Max: s[len(s)-1]}
}
