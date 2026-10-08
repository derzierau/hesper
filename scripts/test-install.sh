#!/bin/bash
# Tests install.sh without installing anything: every run is --dry-run in a
# temporary HOME, and launchctl, codesign, xcrun, sudo, osascript, defaults,
# pgrep and socketfilterfw are stubs that refuse any changing call.
# Checks: syntax, that a dry run changes no file, that a fresh Mac gets every
# step and keeps the user's own files, and that an installed machine needs
# nothing (idempotency). The one real run is the
# Ghosty -> Hesper migration (--migrate-only), in a temporary HOME too.
#
#   scripts/test-install.sh        (builds relay/dist/hesperd if missing)
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
install="$repo/install.sh"
failures=0
pass() { printf 'ok   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }
check() { # check NAME COMMAND…
  local name=$1
  shift
  if "$@"; then pass "$name"; else fail "$name"; fi
}
has() { grep -qF -- "$2" "$1"; }
lacks() { ! grep -qF -- "$2" "$1"; }

check 'sh -n install.sh' sh -n "$install"
check 'bash -n install.sh' bash -n "$install"

if [ ! -x "$repo/relay/dist/hesperd" ]; then
  (cd "$repo/relay" && go build -o dist/hesperd ./cmd/hesperd)
fi
hesperd="$repo/relay/dist/hesperd"

root=$(mktemp -d "${TMPDIR:-/tmp}/hesper-install-test.XXXXXX")
root=$(cd "$root" && pwd -P)
trap '[ -n "${KEEP:-}" ] || rm -rf "$root"' EXIT
stubs="$root/stubs"
mkdir -p "$stubs"
calls="$root/calls.log"
: > "$calls"

# A stub that only allows read-only calls; anything else is logged as
# FORBIDDEN and fails.
forbid() {
  cat > "$stubs/$1" <<EOF
#!/bin/sh
echo "FORBIDDEN $1 \$*" >> "$calls"
exit 99
EOF
  chmod 755 "$stubs/$1"
}
for name in codesign xcrun sudo; do forbid "$name"; done

# The migration test (section 5) runs install.sh --migrate-only for real:
# with $STUB_STATE/allow-migration, the stubs also let ghostyd's LaunchAgent
# stop, Ghosty.app quit and its defaults be copied (recorded in $calls).
cat > "$stubs/launchctl" <<EOF
#!/bin/sh
echo "launchctl \$*" >> "$calls"
if [ "\$1" = print ]; then
  label=\${2##*/}
  grep -qx "\$label" "\$STUB_STATE/loaded" 2>/dev/null
  exit
fi
if [ "\$1" = bootout ] && [ "\${2##*/}" = de.olezierau.ghostyd ] && [ -f "\$STUB_STATE/allow-migration" ]; then
  grep -vx de.olezierau.ghostyd "\$STUB_STATE/loaded" > "\$STUB_STATE/loaded.new" || true
  mv "\$STUB_STATE/loaded.new" "\$STUB_STATE/loaded"
  exit 0
fi
echo "FORBIDDEN launchctl \$*" >> "$calls"
exit 99
EOF
cat > "$stubs/pgrep" <<EOF
#!/bin/sh
[ "\$*" = '-x Ghosty' ] || { echo "FORBIDDEN pgrep \$*" >> "$calls"; exit 99; }
[ -f "\$STUB_STATE/ghosty-running" ]
EOF
cat > "$stubs/osascript" <<EOF
#!/bin/sh
case "\$*" in
  *'tell application id "de.olezierau.ghosty.mac" to quit'*)
    if [ -f "\$STUB_STATE/allow-migration" ]; then
      echo "osascript quit Ghosty" >> "$calls"
      rm -f "\$STUB_STATE/ghosty-running"
      exit 0
    fi ;;
esac
echo "FORBIDDEN osascript \$*" >> "$calls"
exit 99
EOF
cat > "$stubs/socketfilterfw" <<EOF
#!/bin/sh
case "\$1" in
  --getglobalstate) echo 'Firewall is enabled. (State = 1)' ;;
  --getblockall) echo 'Firewall has block all state set to disabled.' ;;
  --listapps)
    echo 'Total number of apps = 2 '
    if [ -f "\$STUB_STATE/fw-allowed" ]; then
      printf '1 : %s \n \t ( Allow incoming connections )\n' "\$(cat "\$STUB_STATE/fw-allowed")"
    fi
    if [ -f "\$STUB_STATE/fw-old" ]; then
      printf '2 : %s \n \t ( Allow incoming connections )\n' "\$(cat "\$STUB_STATE/fw-old")"
    fi ;;
  *) echo "FORBIDDEN socketfilterfw \$*" >> "$calls"; exit 99 ;;
esac
EOF
cat > "$stubs/defaults" <<EOF
#!/bin/sh
case "\$*" in
  'read de.olezierau.ghosty.mac') [ -f "\$STUB_STATE/defaults-ghosty" ]; exit ;;
  'read de.olezierau.hesper.mac') [ -f "\$STUB_STATE/defaults-hesper" ]; exit ;;
  'export de.olezierau.ghosty.mac '*)
    if [ -f "\$STUB_STATE/allow-migration" ]; then
      echo "defaults \$*" >> "$calls"
      cp "\$STUB_STATE/defaults-ghosty" "\$3"
      exit
    fi ;;
  'import de.olezierau.hesper.mac '*)
    if [ -f "\$STUB_STATE/allow-migration" ]; then
      echo "defaults import de.olezierau.hesper.mac" >> "$calls"
      cp "\$3" "\$STUB_STATE/defaults-hesper"
      exit
    fi ;;
esac
echo "FORBIDDEN defaults \$*" >> "$calls"
exit 99
EOF
chmod 755 "$stubs"/*

# run_install NAME HOME [ARGS…]: a dry run with the stubs; output in $root/NAME.out
run_install() {
  local name=$1 home=$2
  shift 2
  set +e
  env -i PATH="/usr/bin:/bin:/usr/sbin:/sbin:$(dirname "$(command -v go)")" TMPDIR="$root" HOME="$home" \
    STUB_STATE="$home/../stub-state" \
    LAUNCHCTL="$stubs/launchctl" CODESIGN="$stubs/codesign" XCRUN="$stubs/xcrun" SUDO="$stubs/sudo" \
    OSASCRIPT="$stubs/osascript" DEFAULTS="$stubs/defaults" PGREP="$stubs/pgrep" \
    SOCKETFILTERFW="$stubs/socketfilterfw" ${EXTRA_ENV:-} \
    "$install" "$@" > "$root/$name.out" 2>&1
  status=$?
  set -e
}

# Every path, link target and file checksum below HOME.
snapshot() {
  (cd "$1" && find . -print | sort && find . -type l -exec sh -c 'printf "%s -> %s\n" "$1" "$(readlink "$1")"' _ {} \; | sort &&
    find . -type f -exec shasum {} + | sort && find . -exec stat -f '%N %Lp' {} + | sort)
}

# --- 1. a fresh Mac with a host enrollment and the user's own files --------

first="$root/first"
H="$first/home"
S="$first/stub-state"
mkdir -p "$H/.local/bin" "$H/.local/lib/hesper" "$H/Library/LaunchAgents" "$H/.local/state/hesper" \
  "$H/.claude" "$H/.codex" "$S"
ln -s /usr/bin/true "$H/.local/bin/hesperctl"                       # a stale link: relinked into the app
echo old > "$H/.local/lib/hesper/hesper-keys"                       # a copy: replaced by a link
echo 'user script' > "$H/.local/bin/mine"                           # not ours
echo '<plist/>' > "$H/Library/LaunchAgents/com.example.mine.plist"  # not ours
chmod 700 "$H/.local/state/hesper"
# A host enrollment (reused), no controller enrollment, devices approved
# here, and the user's own machines.json (never changed).
echo '{"relay":"https://relay.example","deviceId":"dev_laptop_host","role":"host","token":"t"}' > "$H/.local/state/hesper/host.credentials.json"
echo '{"controllers":[]}' > "$H/.local/state/hesper/controllers.json"
mkdir -p "$H/.config/hesper"
echo '{"machines": {"dev_laptop_host": {"short": "L", "glyph": "L", "color": "focus"}}}' > "$H/.config/hesper/machines.json"
# The firewall still lists ghostyd from before the rename.
echo "$H/Applications/Ghosty.app/Contents/MacOS/ghostyd" > "$S/fw-old"
echo '{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}' > "$H/.claude/settings.json"
printf 'model = "gpt-5"\n' > "$H/.codex/config.toml"
mkdir -p "$H/.agents/skills/hesper" "$H/.agents/skills/mine"            # an old copy of the skill (backed up); another skill (kept)
echo old > "$H/.agents/skills/hesper/SKILL.md"

before=$(snapshot "$H")
run_install first "$H" --dry-run --skip-build
out="$root/first.out"
after=$(snapshot "$H")
check 'fresh Mac: dry run exits 0' [ "$status" -eq 0 ]
check 'fresh Mac: dry run changes no file' [ "$before" = "$after" ]
check 'fresh Mac: no changing launchctl/codesign/sudo call' lacks "$calls" FORBIDDEN
check 'fresh Mac: says nothing changed' has "$out" 'Dry run: nothing was changed.'
check 'fresh Mac: nothing of Ghosty to move' has "$out" 'ok: nothing of Ghosty left to move'
check 'fresh Mac: leaves other LaunchAgents' lacks "$out" 'com.example.mine'
check 'fresh Mac: never touches user files' lacks "$out" '~/.local/bin/mine'
check 'fresh Mac: relinks hesperctl into the app' has "$out" 'would: link ~/.local/bin/hesperctl -> ~/Applications/Hesper.app/Contents/MacOS/hesperctl'
check 'fresh Mac: backs up the stale hesperctl link' has "$out" 'would: move ~/.local/bin/hesperctl to the backup'
check 'fresh Mac: links hesperd' has "$out" 'would: link ~/.local/bin/hesperd -> ~/Applications/Hesper.app/Contents/MacOS/hesperd'
check 'fresh Mac: replaces the hesper-keys copy with a link' has "$out" 'would: link ~/.local/lib/hesper/hesper-keys -> ~/Applications/Hesper.app/Contents/MacOS/hesper-keys'
check 'fresh Mac: reuses host credentials' has "$out" 'ok: reusing ~/.local/state/hesper/host.credentials.json (host role)'
check 'fresh Mac: login only for the missing controller role' has "$out" 'login --role controller --name'
check 'fresh Mac: no login for the enrolled host role' lacks "$out" 'login --role host'
check 'fresh Mac: restart hint after sign-in' has "$out" 'then restart hesperd: launchctl kickstart -k gui/'
check "fresh Mac: the user's machines.json is kept" has "$out" 'ok: ~/.config/hesper/machines.json kept as it is (this Mac is L)'
check 'fresh Mac: machines.json not written' lacks "$out" 'would: write ~/.config/hesper/machines.json'
check 'fresh Mac: no host approval hint (controllers.json there)' lacks "$out" 'no device approved on this Mac yet'
check 'fresh Mac: no controller pairing hint (no controller role)' lacks "$out" 'has not paired with another Mac yet'
check 'fresh Mac: names the old ghostyd firewall entry' has "$out" '--remove '"$H"'/Applications/Ghosty.app/Contents/MacOS/ghostyd'
check 'fresh Mac: backs up settings.json first' has "$out" 'would: back up ~/.claude/settings.json'
check 'fresh Mac: installs hooks' has "$out" 'would: install hooks: hesperd hooks install'
check 'fresh Mac: links the skill for Claude Code' has "$out" "would: link ~/.claude/skills/hesper -> $repo/skills/hesper"
check 'fresh Mac: links the skill for Codex' has "$out" "would: link ~/.agents/skills/hesper -> $repo/skills/hesper"
check 'fresh Mac: backs up the old skill copy' has "$out" 'would: move ~/.agents/skills/hesper to the backup'
check "fresh Mac: leaves the user's other skills" lacks "$out" 'skills/mine'
check 'fresh Mac: writes the LaunchAgent' has "$out" 'would: write ~/Library/LaunchAgents/de.olezierau.hesperd.plist'
check 'fresh Mac: loads the LaunchAgent' has "$out" 'would: (re)load de.olezierau.hesperd'
check 'fresh Mac: asks the firewall for hesperd' has "$out" "would: allow hesperd's incoming connections"
check 'fresh Mac: no login item unless asked' lacks "$out" 'login item'

# --- 2b. a Mac with a host enrollment, no machines.json, no approvals --------

fresh="$root/fresh"
H="$fresh/home"
mkdir -p "$H/.local/state/hesper" "$fresh/stub-state"
chmod 700 "$H/.local/state/hesper"
echo '{"relay":"https://relay.example","deviceId":"dev_mini_host","role":"host","token":"t"}' > "$H/.local/state/hesper/host.credentials.json"
echo '{"relay":"https://relay.example","deviceId":"dev_mini_ctl","role":"controller","token":"t"}' > "$H/.local/state/hesper/controller.credentials.json"
: > "$calls"
before=$(snapshot "$H")
run_install fresh "$H" --dry-run --skip-build --machine M --allow-shell
out="$root/fresh.out"
check 'fresh: exits 0' [ "$status" -eq 0 ]
check 'fresh: dry run changes no file' [ "$before" = "$(snapshot "$H")" ]
check 'fresh: no changing call' lacks "$calls" FORBIDDEN
check 'fresh: reuses both enrollments' bash -c "grep -q 'reusing.*host.credentials.json' '$out' && grep -q 'reusing.*controller.credentials.json' '$out'"
check 'fresh: no login hint' lacks "$out" 'login --role'
check 'fresh: writes machines.json for its host device' has "$out" 'would: write ~/.config/hesper/machines.json: this Mac (host device dev_mini_host) is M'
check 'fresh: approval hint (no controllers.json)' has "$out" 'on the other Mac: hesperctl pair-host --machine M'
check 'fresh: pairing hint (no trusted-hosts.json)' has "$out" 'here:             hesperctl pair-host --machine <other Mac>'
EXTRA_ENV="HESPER_STATE_DIR=$H/.local/state/hesper" run_install fresh-plist "$H" --print-launch-agent --allow-shell
check 'fresh: --allow-shell reaches hesperd serve' has "$root/fresh-plist.out" '<string>--allow-shell</string>'
check 'fresh: plist names the config dir' has "$root/fresh-plist.out" "<string>--config-dir</string><string>$H/.config/hesper</string>"
run_install fresh-plain "$H" --print-launch-agent
check 'fresh: no --allow-shell by default' lacks "$root/fresh-plain.out" '--allow-shell'
run_install badmachine "$H" --dry-run --machine 'L"; rm'
check 'a bad --machine exits 2' [ "$status" -eq 2 ]
run_install nomachine "$H" --dry-run --skip-build
check 'fresh without --machine: says how to name it' has "$root/nomachine.out" 'run again with --machine L (or M)'

# --- 3. a machine where Hesper is installed: nothing to do ------------------

inst="$root/installed"
H="$inst/home"
S="$inst/stub-state"
app="$H/Applications/Hesper.app"
mkdir -p "$S" "$app/Contents/MacOS" "$app/Contents/Resources" "$H/.local/bin" "$H/.local/lib/hesper" \
  "$H/Library/LaunchAgents" "$H/.local/state/hesper"
/usr/libexec/PlistBuddy -c 'Add :CFBundleIdentifier string de.olezierau.hesper.mac' "$app/Contents/Info.plist" >/dev/null
for tool in hesperd hesperctl hesper-keys; do printf '#!/bin/sh\n' > "$app/Contents/MacOS/$tool"; chmod 755 "$app/Contents/MacOS/$tool"; done
shasum -a 256 "$hesperd" | cut -d' ' -f1 > "$app/Contents/Resources/hesperd.sha256"
chmod 700 "$H/.local/state/hesper"
ln -s "$app/Contents/MacOS/hesperd" "$H/.local/bin/hesperd"
ln -s "$app/Contents/MacOS/hesperctl" "$H/.local/bin/hesperctl"
ln -s "$app/Contents/MacOS/hesper-keys" "$H/.local/lib/hesper/hesper-keys"
mkdir -p "$H/.claude/skills" "$H/.agents/skills"
ln -s "$repo/skills/hesper" "$H/.claude/skills/hesper"
ln -s "$repo/skills/hesper" "$H/.agents/skills/hesper"
HOME="$H" "$hesperd" hooks install --bin "$H/.local/bin/hesperd" --claude-settings "$H/.claude/settings.json" --codex-home "$H/.codex" > /dev/null
env -i HOME="$H" PATH=/usr/bin:/bin "$install" --print-launch-agent > "$H/Library/LaunchAgents/de.olezierau.hesperd.plist"
echo de.olezierau.hesperd > "$S/loaded"
echo "$app/Contents/MacOS/hesperd" > "$S/fw-allowed"

: > "$calls"
before=$(snapshot "$H")
run_install installed "$H" --dry-run --skip-build
out="$root/installed.out"
after=$(snapshot "$H")
check 'installed: exits 0' [ "$status" -eq 0 ]
check 'installed: dry run changes no file' [ "$before" = "$after" ]
check 'installed: no changing call' lacks "$calls" FORBIDDEN
check 'installed: plist is valid' plutil -lint -s "$H/Library/LaunchAgents/de.olezierau.hesperd.plist"
# Only the build output is (re)installed on every run; everything else is in place.
unexpected=$(grep 'would:' "$out" | grep -vE 'would: (embed relay/dist/|record hesperd|sign |verify the signature|install Hesper.app to)' || true)
check 'installed: nothing else to do (idempotent)' [ -z "$unexpected" ]
[ -z "$unexpected" ] || printf '%s\n' "$unexpected"
check 'installed: links ok' has "$out" 'ok: ~/.local/bin/hesperd -> ~/Applications/Hesper.app/Contents/MacOS/hesperd'
check 'installed: skill links ok' bash -c "grep -qF 'ok: ~/.claude/skills/hesper -> $repo/skills/hesper' '$out' && grep -qF 'ok: ~/.agents/skills/hesper -> $repo/skills/hesper' '$out'"
check 'installed: hooks ok' has "$out" 'ok: ~/.claude/settings.json'
check 'installed: LaunchAgent ok' has "$out" 'ok: de.olezierau.hesperd running, hesperd unchanged'
check 'installed: firewall ok' has "$out" 'ok: hesperd allowed through the firewall'
check 'installed: nothing left alone' lacks "$out" 'left alone'
check 'installed: no relay: says the relay is optional' has "$out" 'note: no relay configured: the agents on this Mac work without one'
check 'installed: no relay: says how to deploy one' has "$out" "deploy your own relay ($repo/relay/docs/deployment.md)"
check 'installed: no relay: says how to set it' has "$out" './install.sh --relay https://relay.example.com'
check 'installed: no relay: no sign-in command yet' lacks "$out" 'login --role host --name'
check 'installed: no relay: the next steps name the deployment doc' bash -c "sed -n '/== Done/,\$p' '$out' | grep -q 'relay/docs/deployment.md'"
check 'installed: no relay: writes no settings' lacks "$out" '.config/hesper/settings.json'
check 'installed: no approval hints without enrollments' lacks "$out" 'pair-host'

# --- 3b. the relay setting (--relay) -----------------------------------------

settings="$H/.config/hesper/settings.json"
: > "$calls"
before=$(snapshot "$H")
run_install relay-new "$H" --dry-run --skip-build --relay https://relay.example.com/
out="$root/relay-new.out"
check 'relay: exits 0' [ "$status" -eq 0 ]
check 'relay: dry run changes no file' [ "$before" = "$(snapshot "$H")" ]
check 'relay: no changing call' lacks "$calls" FORBIDDEN
check 'relay: writes settings.json (no trailing slash)' has "$out" 'would: write ~/.config/hesper/settings.json: relay https://relay.example.com'
check 'relay: then the sign-in commands' bash -c "grep -q 'login --role host --name' '$out' && grep -q 'login --role controller --name' '$out'"
check 'relay: no "no relay" hint' lacks "$out" 'no relay configured'

mkdir -p "$H/.config/hesper"
echo '{"machine": "L", "defaults": {"kind": "codex"}}' > "$settings"
before=$(snapshot "$H")
run_install relay-merge "$H" --dry-run --skip-build --relay https://relay.example.com
out="$root/relay-merge.out"
check 'relay into settings.json: exits 0' [ "$status" -eq 0 ]
check 'relay into settings.json: dry run changes no file' [ "$before" = "$(snapshot "$H")" ]
check 'relay into settings.json: backs it up first' has "$out" 'would: back up ~/.config/hesper/settings.json'
check 'relay into settings.json: sets the key' has "$out" 'would: set relay to https://relay.example.com in ~/.config/hesper/settings.json'

echo '{"machine": "L", "relay": "https://relay.example.com"}' > "$settings"
run_install relay-same "$H" --dry-run --skip-build --relay https://relay.example.com
out="$root/relay-same.out"
check 'relay already set: ok' has "$out" 'ok: relay https://relay.example.com (~/.config/hesper/settings.json)'
unexpected=$(grep 'would:' "$out" | grep -vE 'would: (embed relay/dist/|record hesperd|sign |verify the signature|install Hesper.app to)' || true)
check 'relay already set: nothing else to do' [ -z "$unexpected" ]
run_install relay-kept "$H" --dry-run --skip-build
out="$root/relay-kept.out"
check 'relay from settings.json: shown' has "$out" 'ok: relay https://relay.example.com (~/.config/hesper/settings.json)'
check 'relay from settings.json: sign-in commands' has "$out" 'login --role host --name'
check 'relay from settings.json: no deploy hint' lacks "$out" 'deploy your own relay'

echo '{"machine": ' > "$settings"
run_install relay-badjson "$H" --dry-run --skip-build --relay https://relay.example.com
check 'relay: a broken settings.json is refused' bash -c "[ $status -ne 0 ] && grep -q 'is not valid JSON' '$root/relay-badjson.out'"
rm -f "$settings"

for bad in http://remote.example https://relay.example.com/path 'https://x"; rm -rf ~' relay.example.com; do
  run_install relay-bad "$H" --dry-run --skip-build --relay "$bad"
  check "relay: refuses --relay $bad" [ "$status" -eq 2 ]
done
run_install relay-local "$H" --dry-run --skip-build --relay http://127.0.0.1:8787
check 'relay: loopback HTTP is fine' has "$root/relay-local.out" 'relay http://127.0.0.1:8787'

# Enrolled Macs keep the relay their credentials name.
run_install relay-enrolled "$fresh/home" --dry-run --skip-build --relay https://relay.example.com
out="$root/relay-enrolled.out"
check 'relay on an enrolled Mac: the enrollments keep theirs' bash -c "grep -q 'the host enrollment keeps its relay https://relay.example;' '$out' && grep -q 'the controller enrollment keeps its relay' '$out'"
run_install relay-enrolled-none "$fresh/home" --dry-run --skip-build
check 'enrolled Mac without the setting: no relay hint at all' bash -c "! grep -q 'no relay configured' '$root/relay-enrolled-none.out' && ! grep -q 'deploy your own relay' '$root/relay-enrolled-none.out'"

# --allow-shell stays as installed until --no-allow-shell.
plist_file="$H/Library/LaunchAgents/de.olezierau.hesperd.plist"
cp "$plist_file" "$root/plist.plain"
env -i HOME="$H" PATH=/usr/bin:/bin "$install" --print-launch-agent --allow-shell > "$plist_file"
run_install shell-kept "$H" --dry-run --skip-build
check 'allow-shell: kept on a rerun' has "$root/shell-kept.out" 'ok: ~/Library/LaunchAgents/de.olezierau.hesperd.plist'
run_install shell-off "$H" --dry-run --skip-build --no-allow-shell
check 'allow-shell: --no-allow-shell rewrites the plist' has "$root/shell-off.out" 'would: write ~/Library/LaunchAgents/de.olezierau.hesperd.plist'
cp "$root/plist.plain" "$plist_file"

# A new hesperd build restarts the daemon (agents resume).
echo different > "$app/Contents/Resources/hesperd.sha256"
run_install changed "$H" --dry-run --skip-build
check 'changed hesperd: restart' has "$root/changed.out" 'would: restart hesperd for its new binary'

# --- 4. signing, notarization, login item, refusals -------------------------

EXTRA_ENV="SIGN_IDENTITY=Developer_ID_Test NOTARY_PROFILE=hesper-notary" run_install signed "$H" --dry-run --skip-build --login-item
out="$root/signed.out"
check 'signed: exits 0' [ "$status" -eq 0 ]
check 'signed: hardened runtime for hesperd' has "$out" 'would: sign hesperd (Developer_ID_Test, hardened runtime, timestamp)'
check 'signed: app signed last' bash -c "grep 'would: sign' '$out' | tail -1 | grep -q 'sign Hesper.app'"
check 'signed: notarizes' has "$out" 'would: notarize (notarytool, keychain profile hesper-notary'
check 'signed: staples' has "$out" 'would: staple the ticket'
check 'signed: login item' has "$out" 'would: open Hesper.app at login'
check 'signed: still no changing call' lacks "$calls" FORBIDDEN

EXTRA_ENV="NOTARY_PROFILE=hesper-notary" run_install notary-only "$H" --dry-run --skip-build
check 'notarization without identity is refused' bash -c "[ $status -ne 0 ] && grep -q 'NOTARY_PROFILE needs SIGN_IDENTITY' '$root/notary-only.out'"

run_install no-skills "$H" --dry-run --skip-build --no-skills
check '--no-skills: skips the skill step' bash -c "grep -q 'skipped (--no-skills)' '$root/no-skills.out' && ! grep -q 'skills/hesper' '$root/no-skills.out'"

/usr/libexec/PlistBuddy -c 'Set :CFBundleIdentifier com.example.other' "$app/Contents/Info.plist"
run_install foreign "$H" --dry-run --skip-build
check 'a foreign Hesper.app is never replaced' bash -c "[ $status -ne 0 ] && grep -q 'is not Hesper' '$root/foreign.out'"

run_install badopt "$H" --frobnicate
check 'unknown option exits 2' [ "$status" -eq 2 ]

# --- 5. a Mac that runs Ghosty: the rename to Hesper ------------------------

mig="$root/migrate"
H="$mig/home"
S="$mig/stub-state"
old_app="$H/Applications/Ghosty.app"
st="$H/.local/state/ghosty"
mkdir -p "$S" "$old_app/Contents/MacOS" "$st/attachments/L-1" "$H/.config/ghosty" "$H/.local/lib/ghosty" \
  "$H/.local/bin" "$H/Library/LaunchAgents" "$H/Library/Application Support/Ghosty/Attachments/d-1" "$H/.claude"
chmod 700 "$st"
/usr/libexec/PlistBuddy -c 'Add :CFBundleIdentifier string de.olezierau.ghosty.mac' "$old_app/Contents/Info.plist" >/dev/null
for tool in ghostyd ghostyctl ghosty-keys; do printf '#!/bin/sh\n' > "$old_app/Contents/MacOS/$tool"; done
echo 'secure enclave blob' > "$st/device.key"
echo '{"relay":"https://relay.example","deviceId":"dev_mini_host","role":"host","token":"t"}' > "$st/host.credentials.json"
head -c 4096 /dev/urandom > "$st/history.db"
echo 'shot' > "$st/attachments/L-1/shot.png"
echo 'note' > "$H/Library/Application Support/Ghosty/Attachments/d-1/a.txt"
printf '{"version":1,"agents":[{"id":"L-1","task":"see %s/attachments/L-1/shot.png","running":true}]}\n' "$st" > "$st/agents.json"
esc=$(printf '%s' "$H/Library/Application Support/Ghosty/Attachments/d-1/a.txt" | sed 's|/|\\/|g')
printf '{"version":1,"drafts":[{"id":"d-1","attachments":["%s"]}]}\n' "$esc" > "$st/drafts.json"
echo '{"projects":[]}' > "$st/projects.json"
echo 'old log' > "$st/ghostyd.log"
: > "$st/ghostyd.sock"
echo '{"machines": {"dev_mini_host": {"short": "M"}}}' > "$H/.config/ghosty/machines.json"
ln -s "$old_app/Contents/MacOS/ghosty-keys" "$H/.local/lib/ghosty/ghosty-keys"
ln -s "$old_app/Contents/MacOS/ghostyd" "$H/.local/bin/ghostyd"
ln -s "$old_app/Contents/MacOS/ghostyctl" "$H/.local/bin/ghostyctl"
ln -s "$old_app/Contents/MacOS/ghosty-keys" "$H/.local/bin/ghosty-keys"
ln -s /usr/bin/true "$H/.local/bin/hesper-unrelated"
cat > "$H/Library/LaunchAgents/de.olezierau.ghostyd.plist" <<EOF
<plist><dict><key>Label</key><string>de.olezierau.ghostyd</string><array>
<string>$old_app/Contents/MacOS/ghostyd</string><string>serve</string><string>--allow-shell</string></array></dict></plist>
EOF
HOME="$H" "$hesperd" hooks install --bin "$H/.local/bin/ghostyd" --claude-settings "$H/.claude/settings.json" --codex-home "$H/.codex" > /dev/null
echo de.olezierau.ghostyd > "$S/loaded"
touch "$S/ghosty-running"
echo '<plist/>' > "$S/defaults-ghosty"
sum_before=$(cd "$st" && shasum device.key history.db host.credentials.json attachments/L-1/shot.png)

# A full dry run says what it would do and changes nothing.
: > "$calls"
before=$(snapshot "$H")
run_install migrate-dry "$H" --dry-run --skip-build
out="$root/migrate-dry.out"
check 'migrate dry run: exits 0' [ "$status" -eq 0 ]
check 'migrate dry run: changes no file' [ "$before" = "$(snapshot "$H")" ]
check 'migrate dry run: no changing call' lacks "$calls" FORBIDDEN
check 'migrate dry run: quits Ghosty.app' has "$out" 'would: quit Ghosty.app'
check 'migrate dry run: stops ghostyd' has "$out" 'would: stop the LaunchAgent de.olezierau.ghostyd'
check 'migrate dry run: moves its plist away' has "$out" 'would: move ~/Library/LaunchAgents/de.olezierau.ghostyd.plist to the backup'
check 'migrate dry run: moves the state' has "$out" 'would: move ~/.local/state/ghosty to ~/.local/state/hesper'
check 'migrate dry run: links the old state path' has "$out" 'would: link ~/.local/state/ghosty -> ~/.local/state/hesper'
check 'migrate dry run: renames the log' has "$out" 'would: rename ~/.local/state/hesper/ghostyd.log to hesperd.log'
check 'migrate dry run: drops the socket' has "$out" 'would: remove the stale ~/.local/state/hesper/ghostyd.sock'
check 'migrate dry run: rewrites agents.json' has "$out" 'would: rewrite the Ghosty paths in ~/.local/state/hesper/agents.json'
check 'migrate dry run: rewrites drafts.json (escaped)' has "$out" 'would: rewrite the Ghosty paths in ~/.local/state/hesper/drafts.json'
check 'migrate dry run: leaves projects.json' lacks "$out" 'projects.json'
check 'migrate dry run: moves the config' has "$out" 'would: move ~/.config/ghosty to ~/.config/hesper'
check 'migrate dry run: moves the lib dir' has "$out" 'would: move ~/.local/lib/ghosty to ~/.local/lib/hesper'
check 'migrate dry run: drops the old helper link' has "$out" 'would: remove the old link ~/.local/lib/hesper/ghosty-keys'
check 'migrate dry run: drops the ghostyd link' has "$out" 'would: remove the old link ~/.local/bin/ghostyd'
check 'migrate dry run: removes Ghosty.app' has "$out" 'would: remove ~/Applications/Ghosty.app (Hesper.app replaces it)'
check 'migrate dry run: copies the app settings' has "$out" "would: copy Ghosty.app's settings (de.olezierau.ghosty.mac) to de.olezierau.hesper.mac"
check 'migrate dry run: keeps --allow-shell' has "$out" 'keeps --allow-shell from the installed LaunchAgent'
check 'migrate dry run: sees ghostyd hooks' has "$out" "replaces ghostyd's hooks (Hesper's former name)"
check 'migrate dry run: links hesperd' has "$out" 'would: link ~/.local/bin/hesperd -> ~/Applications/Hesper.app/Contents/MacOS/hesperd'
check 'migrate dry run: loads hesperd' has "$out" 'would: (re)load de.olezierau.hesperd'

# The migration for real (the rest of install.sh needs a build).
touch "$S/allow-migration"
: > "$calls"
run_install migrate "$H" --migrate-only
out="$root/migrate.out"
hs="$H/.local/state/hesper"
check 'migrate: exits 0' [ "$status" -eq 0 ]
[ "$status" -eq 0 ] || cat "$out"
check 'migrate: no forbidden call' lacks "$calls" FORBIDDEN
check 'migrate: quit Ghosty.app' has "$calls" 'osascript quit Ghosty'
check 'migrate: stopped ghostyd' has "$calls" 'launchctl bootout gui/'
check 'migrate: ghostyd unloaded' lacks "$S/loaded" de.olezierau.ghostyd
check 'migrate: old plist gone' [ ! -e "$H/Library/LaunchAgents/de.olezierau.ghostyd.plist" ]
check 'migrate: old plist backed up' bash -c "ls '$H'/.ghosty-config-backups/*/Library/LaunchAgents/de.olezierau.ghostyd.plist"
check 'migrate: state moved (0700)' [ "$(stat -f '%Lp' "$hs")" = 700 ]
check 'migrate: keys, history and enrollments unchanged' [ "$sum_before" = "$(cd "$hs" && shasum device.key history.db host.credentials.json attachments/L-1/shot.png)" ]
check 'migrate: old state path is a link to the new one' [ "$(readlink "$st")" = "$hs" ]
check 'migrate: hesperd.log' bash -c "[ \"\$(cat '$hs/hesperd.log')\" = 'old log' ] && [ ! -e '$hs/ghostyd.log' ]"
check 'migrate: stale socket gone' [ ! -e "$hs/ghostyd.sock" ]
check 'migrate: agents.json points into hesper' bash -c "grep -qF '$hs/attachments/L-1/shot.png' '$hs/agents.json' && ! grep -q state/ghosty '$hs/agents.json'"
check 'migrate: agents.json keeps its mode' [ "$(stat -f '%Lp' "$hs/agents.json")" = "$(stat -f '%Lp' "$hs/projects.json")" ]
esc_new=$(printf '%s' "$H/Library/Application Support/Hesper/Attachments/d-1/a.txt" | sed 's|/|\\/|g')
check 'migrate: drafts.json (escaped) points into Hesper' bash -c "grep -qF '$esc_new' '$hs/drafts.json' && ! grep -q Ghosty '$hs/drafts.json'"
check 'migrate: draft attachments moved' [ -f "$H/Library/Application Support/Hesper/Attachments/d-1/a.txt" ]
check 'migrate: old Application Support path links' [ -f "$H/Library/Application Support/Ghosty/Attachments/d-1/a.txt" ]
check 'migrate: config moved' bash -c "[ -f '$H/.config/hesper/machines.json' ] && [ ! -e '$H/.config/ghosty' ]"
check 'migrate: lib moved, old helper link gone' bash -c "[ -d '$H/.local/lib/hesper' ] && [ ! -e '$H/.local/lib/ghosty' ] && [ ! -L '$H/.local/lib/hesper/ghosty-keys' ]"
check 'migrate: old tool links gone' bash -c "[ ! -L '$H/.local/bin/ghostyd' ] && [ ! -L '$H/.local/bin/ghostyctl' ] && [ ! -L '$H/.local/bin/ghosty-keys' ]"
check 'migrate: unrelated links stay' [ -L "$H/.local/bin/hesper-unrelated" ]
check 'migrate: Ghosty.app removed' [ ! -e "$old_app" ]
check 'migrate: app settings copied' [ -f "$S/defaults-hesper" ]

# Again: nothing left to do.
: > "$calls"
before=$(snapshot "$H")
run_install migrate-again "$H" --migrate-only
check 'migrate again: exits 0' [ "$status" -eq 0 ]
check 'migrate again: nothing to do' has "$root/migrate-again.out" 'ok: nothing of Ghosty left to move'
check 'migrate again: changes nothing' [ "$before" = "$(snapshot "$H")" ]
check 'migrate again: no changing call' bash -c "! grep -E 'FORBIDDEN|bootout|quit|import' '$calls'"

# A fresh Mac (no Ghosty): nothing to do, nothing created.
nog="$root/no-ghosty"
mkdir -p "$nog/home" "$nog/stub-state"
before=$(snapshot "$nog/home")
run_install no-ghosty "$nog/home" --migrate-only
check 'no Ghosty: nothing to do' has "$root/no-ghosty.out" 'ok: nothing of Ghosty left to move'
check 'no Ghosty: creates nothing' [ "$before" = "$(snapshot "$nog/home")" ]

# Both state directories exist: the new one wins, the old one stays.
both="$root/both"
mkdir -p "$both/home/.local/state/ghosty" "$both/home/.local/state/hesper" "$both/stub-state"
echo old > "$both/home/.local/state/ghosty/agents.json"
touch "$both/stub-state/allow-migration"
run_install both "$both/home" --migrate-only
check 'both state dirs: warns' has "$root/both.out" 'both exist: kept ~/.local/state/hesper'
check 'both state dirs: old one untouched' [ "$(cat "$both/home/.local/state/ghosty/agents.json")" = old ]

if [ "$failures" -gt 0 ]; then
  printf '\n%s failure(s); outputs in %s (kept)\n' "$failures" "$root"
  trap - EXIT
  exit 1
fi
printf '\nall install.sh checks passed\n'
