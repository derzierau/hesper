#!/bin/sh
# Set up a pre-signed binary install without changing its app bundle.
set -eu
resources=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
app=$(dirname "$(dirname "$resources")")
bin="$app/Contents/MacOS"
for tool in hesperd hesperctl hesper-keys; do
  [ -x "$bin/$tool" ] || { echo "Missing $tool" >&2; exit 1; }
done
codesign --verify --strict --deep "$app"
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.local/state/hesper" "$HOME/.claude/skills" "$HOME/.agents/skills"
plist="$HOME/Library/LaunchAgents/de.olezierau.hesperd.plist"
allow_shell=0
if [ -f "$plist" ] && plutil -extract ProgramArguments json -o - "$plist" | grep -q -- '"--allow-shell"'; then
  allow_shell=1
fi
staged_plist=$(mktemp "$HOME/Library/LaunchAgents/.hesper.XXXXXX")
trap 'rm -f "$staged_plist"' EXIT
plutil -create xml1 "$staged_plist"
plutil -insert Label -string de.olezierau.hesperd "$staged_plist"
plutil -insert ProgramArguments -xml '<array/>' "$staged_plist"
plutil -insert ProgramArguments.0 -string "$bin/hesperd" "$staged_plist"
plutil -insert ProgramArguments.1 -string serve "$staged_plist"
if [ "$allow_shell" -eq 1 ]; then
  plutil -insert ProgramArguments.2 -string "--allow-shell" "$staged_plist"
fi
plutil -insert RunAtLoad -bool YES "$staged_plist"
plutil -insert KeepAlive -bool YES "$staged_plist"
plutil -insert StandardOutPath -string "$HOME/.local/state/hesper/daemon.log" "$staged_plist"
plutil -insert StandardErrorPath -string "$HOME/.local/state/hesper/daemon.log" "$staged_plist"
plutil -lint "$staged_plist"
mv "$staged_plist" "$plist"
"$bin/hesperd" hooks install --bin "$bin/hesperd"
for target in "$HOME/.claude/skills/hesper" "$HOME/.agents/skills/hesper"; do
  if [ -e "$target" ] && [ ! -L "$target" ]; then
    echo "Keeping existing skill directory: $target"
  else
    ln -sfn "$resources/hesper-skill" "$target"
  fi
done
domain="gui/$(id -u)"
launchctl bootout "$domain/de.olezierau.hesperd" 2>/dev/null || true
launchctl bootstrap "$domain" "$plist"
launchctl kickstart "$domain/de.olezierau.hesperd"
echo 'Hesper is ready. In Codex, enable hooks once using /hooks.'
