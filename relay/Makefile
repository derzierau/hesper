.PHONY: build keys test check latency
UNAME := $(shell uname -s)
MACOS_MIN_VERSION ?= 14.0
SWIFT_TARGET ?= $(shell uname -m)-apple-macosx$(MACOS_MIN_VERSION)

build: keys
	mkdir -p dist
	go build -trimpath -o dist/ ./cmd/...

# hesper-keys (Secure Enclave device keys) is a Swift helper, macOS only.
keys:
ifeq ($(UNAME),Darwin)
	mkdir -p dist
	swiftc -O -target $(SWIFT_TARGET) -o dist/hesper-keys macos/hesper-keys/main.swift
else
	@echo "hesper-keys: not macOS, skipped (controllers use software keys)"
endif

test:
	go test -race ./...
check:
	go vet ./...

# Keystroke/request latency through an in-process relay with 20 ms legs
# (HESPER_LATENCY_LEG=40ms, HESPER_LATENCY_KEYS=500 to vary); prints a table.
latency:
	go test ./internal/transport -run '^TestLatency$$' -bench Latency -count 1
