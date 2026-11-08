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
# hesperd serve's flags (--allow-shell, --allow-forward PORTS, …) are kept:
# from the installed plist, or from the copy saved by the last setup when
# Homebrew's uninstall step deleted the plist during an upgrade.
saved_flags="$HOME/.local/state/hesper/serve-flags"
staged_plist=$(mktemp "$HOME/Library/LaunchAgents/.hesper.XXXXXX")
flags=$(mktemp "$HOME/.local/state/hesper/.serve-flags.XXXXXX")
trap 'rm -f "$staged_plist" "$flags"' EXIT
if [ -f "$plist" ]; then
  i=2
  # One per line; plutil ends raw output with a newline only on newer macOS.
  while flag=$(plutil -extract "ProgramArguments.$i" raw -o - "$plist" 2>/dev/null); do
    printf '%s\n' "$flag" >> "$flags"
    i=$((i + 1))
  done
elif [ -f "$saved_flags" ]; then
  cp "$saved_flags" "$flags"
fi
plutil -create xml1 "$staged_plist"
plutil -insert Label -string de.olezierau.hesperd "$staged_plist"
plutil -insert ProgramArguments -xml '<array/>' "$staged_plist"
plutil -insert ProgramArguments.0 -string "$bin/hesperd" "$staged_plist"
plutil -insert ProgramArguments.1 -string serve "$staged_plist"
i=2
while IFS= read -r flag; do
  plutil -insert "ProgramArguments.$i" -string "$flag" "$staged_plist"
  i=$((i + 1))
done < "$flags"
plutil -insert RunAtLoad -bool YES "$staged_plist"
plutil -insert KeepAlive -bool YES "$staged_plist"
plutil -insert StandardOutPath -string "$HOME/.local/state/hesper/daemon.log" "$staged_plist"
plutil -insert StandardErrorPath -string "$HOME/.local/state/hesper/daemon.log" "$staged_plist"
plutil -lint "$staged_plist"
mv "$staged_plist" "$plist"
mv "$flags" "$saved_flags"
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
# bootout returns before the old daemon is gone, and bootstrapping meanwhile
# fails with "Input/output error": wait for it, at most 10 s.
tries=0
while [ "$tries" -lt 50 ] && launchctl print "$domain/de.olezierau.hesperd" >/dev/null 2>&1; do
  sleep 0.2
  tries=$((tries + 1))
done
launchctl bootstrap "$domain" "$plist"
launchctl kickstart "$domain/de.olezierau.hesperd"
echo 'Hesper is ready. In Codex, enable hooks once using /hooks.'
