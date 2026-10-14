# Reel

macOS screen recorder software for the things that are easier to show than to type.

Pick a display, a window or a part of the screen, press ⌃⌥R and talk. Reel records the screen, what your Mac is playing and what you're saying, all in sync, and when you stop, the video is already on your clipboard: paste it into Slack, an email or a pull request. No account, no upload, no editor to get through first.

- **Your voice and your Mac's sound, together.** Talk over a video, a demo with sound or a call. Both end up in the recording, in sync from the first second to the last.
- **Any part of the screen.** A whole display, one window, or an area you drag out.
- **Pause when you need to.** The pause is cut out cleanly, and the recording picks up with what's on screen when you resume.
- **Out of the way.** A small floating control shows the time, pause and stop. ⌃⌥R works from any app, and Reel's own windows never show up in a recording.
- **An MP4 that plays anywhere,** written as it records, so a crash costs seconds, not the recording.

**Status:** early. The recording pipeline is done and tested (milestones M0–M4); the app (M5) and trimming (M6) are being tried out. The design and milestones are in [docs/plan.md](docs/plan.md).

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
- `AVAssetWriter` encodes H.264 and AAC in five-second fragments, so a crash loses at most the last few seconds; at the end the index moves to the front of the file, so it starts playing before it has fully downloaded.
- `reel selftest` records the app's own flashing, clicking window (plus a fake microphone) and checks the file: structure, codec, duration, and audio/video sync.

[AGENTS.md](AGENTS.md) describes the packages and the rules for working on them.

## License

MIT; see [LICENSE](LICENSE). The bundled fonts, Inter and JetBrains Mono, are under the SIL Open Font License ([internal/text/fonts](internal/text/fonts)). SDL3 is under the zlib license.
