# app/: performance and UI

- Nothing on the main actor may block: no file IO, socket reads or decoding
  of large payloads there. Do the work off the main actor and publish the
  result back on `@MainActor`.
- Views redraw only for changes they show: observe the narrowest model, not
  the whole store.
- Terminal drawing belongs to libghostty (`Sources/Hesper/Engine/`). Add no
  per-frame work around the surface.
- Lists that can hold thousands of sessions (History, walls) are lazy and do
  not sort or filter on every render.
- Perf evidence comes from the harness in `Sources/Hesper/Perf/`:
  `make -C app perf` writes `build/perf-last.json`. It opens windows, so ask
  the user to run it; never start it yourself.
- Logic that can be tested headless belongs in `Sources/HesperCore` with a
  test in `Tests/HesperCoreTests`.
