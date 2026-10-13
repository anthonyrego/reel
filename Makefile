# Dev builds keep their data, logs and recordings in .dev/ so they never
# touch the installed app's settings or ~/Movies/Reel. Override with
# REEL_DATA_DIR=... make run.
REEL_DATA_DIR ?= $(CURDIR)/.dev/data
export REEL_DATA_DIR

# The certificate dev bundles are signed with (its SHA-1): an Apple
# Development certificate. Permissions are kept against it, so they survive
# rebuilds; ad hoc ("-") works, but macOS asks again after every build. Put
# SIGN = <SHA-1> in local.mk, which git ignores (security find-identity -v -p codesigning).
-include local.mk
SIGN ?= -
export SIGN

.PHONY: build app run render selftest record micsync test vet shaders shaders-check check

build:
	go build -o build/reel ./cmd/reel

# The signed bundle. Run it with `make run`, never as a bare binary: a
# binary started from a terminal records with the terminal's permissions.
app: build
	./scripts/make-app.sh build/reel build/Reel.app

# Opens the bundle through Launch Services, so the permissions are the
# app's; the data dir goes in through --env, since open doesn't pass the
# environment. Logs: .dev/data/logs/app.log.
run: app
	open -n --env REEL_DATA_DIR=$(REEL_DATA_DIR) build/Reel.app --args $(ARGS)

# Records the beacon (reel's own window) and a fake mic, then checks the
# file (agents: safe; needs ffmpeg). ARGS="-for 8s -video-only" etc.
selftest: build
	./build/reel selftest $(ARGS) </dev/null

# User only: a real recording of a display (or window) with a mic, to
# .dev/data/recordings. ARGS="-display main -mic default -for 60s".
record: build
	./build/reel record $(ARGS)

# User only: clicks through the speakers; how late a mic hears them.
# ARGS="-mic <ID from ./build/reel devices>".
micsync: build
	./build/reel micsync $(ARGS)

# Offscreen PNG of the home scene -> build/render.png (no window).
render: build
	./build/reel render -out build/render.png $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

# Rebuild shaders after editing shaders/src (needs: brew install glslang spirv-cross).
shaders:
	./scripts/build-shaders.sh

shaders-check:
	./scripts/check-shaders.sh

# Everything that must pass before a commit.
check: vet test shaders-check build
