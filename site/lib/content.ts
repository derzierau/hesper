/** Facts every section may cite. Each value exists in README.md or a doc; do not invent numbers. */
export const site = {
  repo: "https://github.com/derzierau/hesper",
  releases: "https://github.com/derzierau/hesper/releases",
  security: "https://github.com/derzierau/hesper/security/advisories/new",
  contributing: "https://github.com/derzierau/hesper/blob/main/CONTRIBUTING.md",
  architecture: "https://github.com/derzierau/hesper/blob/main/relay/docs/architecture.md",
  protocol: "https://github.com/derzierau/hesper/blob/main/relay/docs/protocol.md",
  deployment: "https://github.com/derzierau/hesper/blob/main/relay/docs/deployment.md",
  brew: [
    "brew tap derzierau/hesper https://github.com/derzierau/hesper.git",
    "brew install --cask derzierau/hesper/hesper",
    "/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh",
    "open /Applications/Hesper.app",
  ],
  version: "0.3.0",
  requirements: ["macOS 14 or newer", "Apple Silicon or Intel", "Claude Code or Codex, installed separately"],
  speed: [
    { what: "Keystroke to frame", condition: "local agent, p50", result: "2.7 ms" },
    { what: "Wall frame rate", condition: "16 live agents", result: "120 fps, 0 dropped" },
    { what: "Remote keystroke echo", condition: "same LAN / via relay", result: "0.4 ms / 45 ms" },
    { what: "Daemon CPU", condition: "30 idle agents, 2 s window", result: "0.15 ms" },
    { what: "State change on another Mac", condition: "to your wall, p50", result: "22 ms" },
  ],
  tests: { go: 275, swift: 193 },
} as const;
