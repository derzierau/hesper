# Hesper.app — native macOS view of hesperd (part A of docs/rebuild-contract.md).
#
#   make build      release build → build/Hesper.app (ad-hoc signed)
#   make test       unit tests (headless) + fake daemon vet + UI smoke test
#   make test-unit  unit tests only
#   make test-ui    launch the app against the fake daemon and drive it
#   make test-ui-real  the same against the real hesperd (relay/dist), fake agents
#   make perf       Phase 0 measurement: 16 live tiles, latency, memory
#   make test-ui-windows  agent windows, walls, scopes, routing, relaunch (SHOTS=dir)
#   make test-ui-projects  projects: bands, sidebar, home wall (SHOTS=dir)
#   make test-ui-desks  desks per display setup, pointers (SHOTS=dir)
#   make perf-walls the same agents on 1 wall, then 3 walls (PERF_WALLS)
#   make test-ui-history  shared history: ⌘Y, card actions, ghost cards, ⌘K (SHOTS=dir)
#   make perf-history  History's budgets, then the wall with History open + events
#   make run        run against the fake daemon (DAEMON=live: the real socket)
#   make layout     layout dump + screenshot (AGENTS, WINDOW_SIZE, ARRANGEMENT, SHOW, SHOT)
#   make layout-matrix  the same over arrangements x counts x sizes (OUT=dir)
#
# Everything uses the Xcode toolchain without opening Xcode. Nothing here
# touches ~/.local/state, LaunchAgents or real agents; the fake daemon and
# its socket live in a fresh temp dir.

SHELL := /bin/bash
export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

VERSION ?= 0.1.0
BUILD_NUMBER ?= $(shell git rev-list --count HEAD 2>/dev/null || echo 1)
CONFIG ?= release
APP := build/Hesper.app
BIN := .build/$(CONFIG)/Hesper
FAKE := .build/fake-hesperd
SIGN_IDENTITY ?= -

PERF_AGENTS ?= 16
PERF_TUI ?= flood
PERF_FPS ?= 120
PERF_SECONDS ?= 10
DAEMON ?= fake

.PHONY: test-ui-history perf-history test-ui-desks test-ui-projects test-ui-windows perf-walls layout layout-matrix build app bin fake test test-unit test-fake test-ui test-ui-real perf run clean icon icon-preview

build: app fake

# SWIFT_BUILD_FLAGS=--disable-sandbox where SwiftPM's sandbox can't start
# (e.g. a remote shell outside the login session).
SWIFT_BUILD_FLAGS ?=

# libghostty built from source without libintl (../scripts/build-ghosttykit.sh,
# ../docs/licensing.md) when it exists; else Package.swift's prebuilt one.
HESPER_GHOSTTYKIT ?= $(if $(wildcard Vendor/GhosttyKit.xcframework),Vendor/GhosttyKit.xcframework)
export HESPER_GHOSTTYKIT

bin:
	swift build -c $(CONFIG) --product Hesper $(SWIFT_BUILD_FLAGS)

fake: $(FAKE)

$(FAKE): $(wildcard Tools/fake-hesperd/*.go) Tools/fake-hesperd/go.mod
	cd Tools/fake-hesperd && GOFLAGS=-mod=mod go build -o ../../$(FAKE) .

build/AppIcon.icns: Tools/make-icon.swift Resources/Fonts/Geist-VF.ttf
	@mkdir -p build
	rm -rf build/AppIcon.iconset
	swift Tools/make-icon.swift build/AppIcon.iconset
	iconutil -c icns build/AppIcon.iconset -o $@

# The 1024 px preview committed under Resources/brand (refresh after
# changing the generator).
icon-preview:
	swift Tools/make-icon.swift build/AppIcon.iconset Resources/brand/icon-1024.png

icon: build/AppIcon.icns

app: bin build/AppIcon.icns
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp $(BIN) $(APP)/Contents/MacOS/Hesper
	sed -e 's/__VERSION__/$(VERSION)/' -e 's/__BUILD__/$(BUILD_NUMBER)/' Resources/Info.plist > $(APP)/Contents/Info.plist
	cp build/AppIcon.icns $(APP)/Contents/Resources/AppIcon.icns
	mkdir -p $(APP)/Contents/Resources/Fonts
	cp Resources/Fonts/Geist-VF.ttf Resources/Fonts/GeistMono-VF.ttf Resources/Fonts/OFL.txt $(APP)/Contents/Resources/Fonts/
	cp ../LICENSE ../THIRD_PARTY_NOTICES.md $(APP)/Contents/Resources/
	printf 'APPL????' > $(APP)/Contents/PkgInfo
	codesign --force --sign "$(SIGN_IDENTITY)" --timestamp=none $(APP)
	codesign --verify --strict $(APP)
	@echo "built $(APP)"

test: test-unit test-fake test-ui test-ui-windows test-ui-projects test-ui-desks test-ui-history

test-unit:
	swift test

test-fake:
	cd Tools/fake-hesperd && gofmt -l . | (! grep .) && go vet ./...

test-ui: build
	Tools/run-with-fake.sh selftest

# Multi-window smoke test: phase 1, then a relaunch on the same window state.
test-ui-windows: build
	SHOTS=$(SHOTS) Tools/run-windows.sh selftest

# Projects (views): bands, collapse, ＋, sidebar, home wall, layouts (SHOTS=dir).
test-ui-projects: build
	SHOTS=$(SHOTS) Tools/run-projects.sh

# Desks: per display setup (injected display sets), pointers, relaunch (SHOTS=dir).
test-ui-desks: build
	SHOTS=$(SHOTS) Tools/run-desks.sh
# Shared history: History panel, card, ghost cards, ⌘K (SHOTS=dir), then
# the same app against a daemon without sessions.* (History hidden).
test-ui-history: build
	SHOTS=$(SHOTS) Tools/run-history.sh test

# History's budgets (3000 sessions), then the wall's numbers with History
# open while sessions.changed/indexing stream (PERF_HISTORY=1), and without.
perf-history: build
	Tools/run-history.sh perf
	$(MAKE) -C ../relay build
	Tools/run-history.sh real-perf
	PERF_HISTORY=1 FAKE_ARGS="--sessions 3000" PERF_AGENTS=$(PERF_AGENTS) PERF_TUI=$(PERF_TUI) PERF_FPS=$(PERF_FPS) PERF_SECONDS=$(PERF_SECONDS) Tools/run-with-fake.sh perf
	cp build/perf-last.json build/perf-wall-history-open.json
	FAKE_ARGS="--sessions 3000" PERF_AGENTS=$(PERF_AGENTS) PERF_TUI=$(PERF_TUI) PERF_FPS=$(PERF_FPS) PERF_SECONDS=$(PERF_SECONDS) Tools/run-with-fake.sh perf
	cp build/perf-last.json build/perf-wall-history-closed.json

perf-walls: build
	PERF_WALLS=$(or $(PERF_WALLS),3) PERF_AGENTS=$(PERF_AGENTS) PERF_SECONDS=$(PERF_SECONDS) Tools/run-windows.sh perf

# The same smoke test against the real hesperd (part D) with fake profiles.
test-ui-real: build
	$(MAKE) -C ../relay build
	Tools/run-with-fake.sh selftest-real
	Tools/run-history.sh real-test

perf: build
	PERF_AGENTS=$(PERF_AGENTS) PERF_TUI=$(PERF_TUI) PERF_FPS=$(PERF_FPS) PERF_SECONDS=$(PERF_SECONDS) Tools/run-with-fake.sh perf

# Layout dump (+ screenshot with SHOT=path): AGENTS=6 WINDOW_SIZE=1600x1000
layout: build
	Tools/run-with-fake.sh layout

# Dumps + screenshots over arrangements x agent counts x window sizes (OUT=dir)
layout-matrix: build
	Tools/layout-matrix.sh

run: build
ifeq ($(DAEMON),live)
	$(APP)/Contents/MacOS/Hesper
else
	Tools/run-with-fake.sh run
endif

clean:
	rm -rf build .build/fake-hesperd
	swift package clean
