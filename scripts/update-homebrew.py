#!/usr/bin/env python3
"""Generate the tap cask from the exact signed release archives."""
import hashlib
import json
import re
import sys
from pathlib import Path

tag, directory = sys.argv[1:3]
metadata = json.loads(Path(sys.argv[3]).read_text()) if len(sys.argv) > 3 else None
if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", tag):
    raise SystemExit("Expected a stable vX.Y.Z tag")
version = tag[1:]
output = Path(__file__).resolve().parent.parent / "Casks/hesper.rb"
# Re-running an older release must not downgrade the tap.
if output.exists():
    current = re.search(r'version "([0-9.]+)"', output.read_text())
    if current and tuple(map(int, current[1].split("."))) > tuple(map(int, version.split("."))):
        raise SystemExit("Refusing to downgrade Homebrew")
checksums = {arch: hashlib.sha256((Path(directory) / f"Hesper-{tag}-{arch}.zip").read_bytes()).hexdigest()
             for arch in ("arm64", "x86_64")}
download = '  url "https://github.com/derzierau/hesper/releases/download/v#{version}/Hesper-v#{version}-#{arch}.zip"'
if metadata:
    assets = {a["name"]: a["id"] for a in metadata["assets"]}
    ids = {arch: assets[f"Hesper-{tag}-{arch}.zip"] for arch in checksums}
    download = f'''  on_arm do
    url "https://api.github.com/repos/derzierau/hesper/releases/assets/{ids['arm64']}",
        header: ["Accept: application/octet-stream", "Authorization: Bearer #{{ENV['HOMEBREW_GITHUB_API_TOKEN']}}"]
  end
  on_intel do
    url "https://api.github.com/repos/derzierau/hesper/releases/assets/{ids['x86_64']}",
        header: ["Accept: application/octet-stream", "Authorization: Bearer #{{ENV['HOMEBREW_GITHUB_API_TOKEN']}}"]
  end'''
output.parent.mkdir(exist_ok=True)
output.write_text(f'''cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "{version}"
  sha256 arm: "{checksums['arm64']}",
         intel: "{checksums['x86_64']}"

{download}
  name "Hesper"
  desc "Run and steer coding agents across your Macs"
  homepage "https://github.com/derzierau/hesper"
  depends_on macos: ">= :sonoma"
  container type: :zip

  app "Hesper.app"
  binary "#{{appdir}}/Hesper.app/Contents/MacOS/hesperctl"
  binary "#{{appdir}}/Hesper.app/Contents/MacOS/hesperd"
  binary "#{{appdir}}/Hesper.app/Contents/MacOS/hesper-keys"

  uninstall launchctl: "de.olezierau.hesperd"
  caveats <<~EOS
    Set up the daemon, agent hooks and skill after installing or upgrading:
      /bin/sh "#{{appdir}}/Hesper.app/Contents/Resources/hesper-setup.sh"
    In Codex, enable the hooks once with /hooks.
  EOS
end
''')
