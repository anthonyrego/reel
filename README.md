# Reel

A screen recorder for macOS that mixes what your Mac plays with your microphone into **one** audio track, then puts the MP4 on your clipboard, ready to paste into Slack.

- Record a whole display, one window, or an area you drag out.
- System audio and a mic, mixed: talk over a demo, a video or a call. Each source is placed by its own timestamps, with its clock drift corrected, so they stay in sync.
- Pause and resume: the pause is cut out, and the picture after it is the one on screen when you resume.
- ⌃⌥R starts and stops from any app; a small floating control shows the time, pause and stop while recording.
- H.264 and AAC in a fast-start MP4, written in five-second fragments, so a crash loses seconds, not the recording.

**Status:** early. The recording pipeline is done and tested (milestones M0–M4); the app (M5) is being tried out; trimming (M6) is next. The design and milestones are in [docs/plan.md](docs/plan.md).

## Requirements

- macOS 15 or later (ScreenCaptureKit records a microphone from 15.0). Built and tested on Apple silicon.
- Go 1.26.5 or later (`GOTOOLCHAIN=auto` fetches it) and the Xcode Command Line Tools.
- For the self-tests only: `ffmpeg` and `ffprobe` (`brew install ffmpeg`).

## Build and run

```sh
make check   # vet, tests, the shader manifest, a build
make run     # build Reel.app, sign it and open it
```

macOS keeps the Screen Recording and Microphone permissions against the app's signature. By default the bundle is signed ad hoc, so macOS asks again after every build. To keep them, sign with your Apple Development certificate: put its SHA-1 in `local.mk` (git ignores it), which the Makefile reads.

```sh
security find-identity -v -p codesigning     # find the SHA-1
echo 'SIGN = <SHA-1>' > local.mk
```

Development runs keep their settings, logs and recordings in `.dev/data`; the installed app uses `~/Library/Application Support/Reel` and `~/Movies/Reel`.

## How it works

Go throughout, with no cgo. Reel draws its own interface with SDL3's GPU API ([go-sdl3](https://github.com/Zyko0/go-sdl3)) and calls ScreenCaptureKit, AVFoundation and CoreMedia through [purego](https://github.com/ebitengine/purego)'s Objective-C runtime bindings.

- One `SCStream` records the screen, system audio and the microphone (two for a window: a window's stream only hears its own app).
- A pure-Go mixer places each audio source on a 48 kHz timeline by its timestamps, through a resampler whose rate is bent slightly to absorb clock drift, and sums them through a soft limiter.
- `AVAssetWriter` encodes H.264 and AAC; the index is moved to the front of the file at the end.
- `reel selftest` records the app's own flashing, clicking window (plus a fake microphone) and checks the file: structure, codec, duration, and audio/video sync.

[AGENTS.md](AGENTS.md) describes the packages and the rules for working on them.

## License

MIT; see [LICENSE](LICENSE). The bundled fonts, Inter and JetBrains Mono, are under the SIL Open Font License ([internal/text/fonts](internal/text/fonts)). SDL3 is under the zlib license.
