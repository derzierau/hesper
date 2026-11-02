# Hesper for iPhone and iPad (apps/ios). Headless: nothing here opens a
# window or boots a simulator UI.
#
#   make project   generate Hesper.xcodeproj from project.yml (XcodeGen)
#   make test      the logic's tests (swift test, on this Mac)
#   make build     build the app and its extensions for the simulator, unsigned
#   make lint      swift format lint over the iOS sources
#   make format    swift format in place
#   make check     lint, test and build: what CI runs

SHELL := /bin/bash
export DEVELOPER_DIR ?= /Applications/Xcode.app/Contents/Developer

DERIVED ?= build/DerivedData
DESTINATION ?= generic/platform=iOS Simulator
SOURCES := App Widgets NotificationService Packages/HesperKit/Sources Packages/HesperKit/Tests Packages/HesperUI/Sources

.PHONY: project test build lint format check clean

project:
	@command -v xcodegen >/dev/null || { echo "xcodegen not found: brew install xcodegen" >&2; exit 1; }
	xcodegen generate --quiet

test:
	swift test --package-path Packages/HesperKit

build: project
	set -o pipefail; xcodebuild build -project Hesper.xcodeproj -scheme Hesper \
		-destination '$(DESTINATION)' -derivedDataPath $(DERIVED) \
		CODE_SIGNING_ALLOWED=NO SWIFT_TREAT_WARNINGS_AS_ERRORS=YES -quiet

lint:
	swift format lint --strict --recursive --parallel $(SOURCES)

format:
	swift format --in-place --recursive --parallel $(SOURCES)

check: lint test build

clean:
	rm -rf build Hesper.xcodeproj
