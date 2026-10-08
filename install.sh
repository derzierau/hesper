#!/bin/sh
# Hesper installer (part C of docs/rebuild-contract.md).
#
# Builds hesperd and Hesper.app from this checkout, installs the app, runs
# hesperd as a LaunchAgent and installs the Claude/Codex hooks. On a Mac
# that ran Ghosty (Hesper's former name) it first moves ghostyd's
# LaunchAgent, state, config and app over to Hesper (migrate_from_ghosty).
# Safe to run again: every step checks first and only changes what differs.
# --dry-run prints every action and changes nothing.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() {
  cat <<'EOF'
usage: ./install.sh [--dry-run] [--skip-build] [--login-item] [--no-firewall]
                    [--machine SHORT] [--allow-shell | --no-allow-shell] [--mcp]
                    [--no-skills] [--relay URL]

  --dry-run            print every action, change nothing
  --skip-build         use the existing relay/dist and app/build outputs
  --login-item         also open Hesper.app at login
  --no-firewall        do not let hesperd through the macOS firewall
  --no-skills          do not link the Hesper skill (skills/hesper) into
                       ~/.claude/skills and ~/.agents/skills (Codex)
  --machine SHORT      this Mac's short name on your other Macs (e.g. L or M):
                       written to ~/.config/hesper/machines.json for its host
                       enrollment, only when that file does not exist yet
  --allow-shell        let devices approved here with the shell right start
                       shells on this Mac (hesperd serve --allow-shell; Touch
                       ID on theirs). --no-allow-shell turns it off again;
                       without either, the installed LaunchAgent's choice stays
  --mcp                register hesperctl mcp (Hesper as MCP tools) with Claude
                       Code (user scope) and Codex (~/.codex/config.toml)
  --relay URL          your relay's origin (https://relay.example.com): written
                       to ~/.config/hesper/settings.json ("relay"), where
                       hesperctl login finds it. The relay is one you deploy
                       yourself (relay/docs/deployment.md), needed only for
                       agents across Macs; this Mac's agents work without one

Environment:
  SIGN_IDENTITY   codesign identity ("Developer ID Application: …"): signs
                  the app and its tools with hardened runtime and a secure
                  timestamp. Unset: ad-hoc signature.
  NOTARY_PROFILE  notarytool keychain profile (xcrun notarytool
                  store-credentials); notarizes and staples the app. Needs
                  SIGN_IDENTITY.
  HESPER_APP_DIR  where Hesper.app goes (default ~/Applications)
  HESPER_STATE_DIR  hesperd's state (default ~/.local/state/hesper)
  HESPER_CONFIG_DIR hesperd's config (default ~/.config/hesper)
EOF
}

dry_run=0
skip_build=0
login_item=0
firewall=1
skills=1
print_plist=0
migrate_only=0
allow_shell='' # '': keep what the installed LaunchAgent has
machine_short=''
mcp=0
relay=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --skip-build) skip_build=1 ;;
    --login-item) login_item=1 ;;
    --no-firewall) firewall=0 ;;
    --no-skills) skills=0 ;;
    --allow-shell) allow_shell=1 ;;
    --no-allow-shell) allow_shell=0 ;;
    --mcp) mcp=1 ;;
    --machine)
      [ "$#" -ge 2 ] || { printf 'install.sh: --machine needs a short name\n' >&2; exit 2; }
      machine_short=$2
      shift
      ;;
    --machine=*) machine_short=${1#--machine=} ;;
    --relay)
      [ "$#" -ge 2 ] || { printf 'install.sh: --relay needs a URL\n' >&2; exit 2; }
      relay=$2
      shift
      ;;
    --relay=*) relay=${1#--relay=} ;;
    --print-launch-agent) print_plist=1 ;; # for tests: the plist, nothing else
    --migrate-only) migrate_only=1 ;; # for tests: the Ghosty → Hesper migration, nothing else
    --help|-h) usage; exit 0 ;;
    *)
      printf 'install.sh: unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

# External programs, replaceable for tests (scripts/test-install.sh).
LAUNCHCTL=${LAUNCHCTL:-launchctl}
CODESIGN=${CODESIGN:-codesign}
XCRUN=${XCRUN:-xcrun}
SOCKETFILTERFW=${SOCKETFILTERFW:-/usr/libexec/ApplicationFirewall/socketfilterfw}
SUDO=${SUDO:-sudo}
OSASCRIPT=${OSASCRIPT:-osascript}
DEFAULTS=${DEFAULTS:-defaults}
PGREP=${PGREP:-pgrep}
CLAUDE=${CLAUDE:-claude}

app_dir=${HESPER_APP_DIR:-$HOME/Applications}
app="$app_dir/Hesper.app"
state_dir=${HESPER_STATE_DIR:-$HOME/.local/state/hesper}
config_dir=${HESPER_CONFIG_DIR:-$HOME/.config/hesper}
bin_dir="$HOME/.local/bin"
lib_dir="$HOME/.local/lib/hesper"
staged="$repo_dir/app/build/Hesper.app"
bundle_id=de.olezierau.hesper.mac
label=de.olezierau.hesperd
plist="$HOME/Library/LaunchAgents/$label.plist"
tools="hesperd hesperctl hesper-keys"
uid=$(id -u)
backup_root="$HOME/.ghosty-config-backups/$(date '+%Y%m%d-%H%M%S')"
backup_count=0
# Hesper was called Ghosty (see migrate_from_ghosty).
old_label=de.olezierau.ghostyd
old_plist="$HOME/Library/LaunchAgents/$old_label.plist"
old_bundle_id=de.olezierau.ghosty.mac
old_app="$app_dir/Ghosty.app"
old_state_dir="$HOME/.local/state/ghosty"
old_config_dir="$HOME/.config/ghosty"
old_lib_dir="$HOME/.local/lib/ghosty"
old_support="$HOME/Library/Application Support/Ghosty"
support="$HOME/Library/Application Support/Hesper"
case $machine_short in
  *[!A-Za-z0-9_-]*) printf 'install.sh: --machine: letters, digits, - and _ only\n' >&2; exit 2 ;;
esac
# --relay: an HTTPS origin (HTTP only on loopback), no path, as hesperctl
# checks it (relay/pkg/client Origin).
relay=${relay%/}
if [ -n "$relay" ]; then
  case $relay in
    *[!A-Za-z0-9.:/_-]*) relay_ok=0 ;;
    https://*/*|http://*/*) relay_ok=0 ;;
    https://?*|http://localhost|http://localhost:*|http://127.0.0.1|http://127.0.0.1:*) relay_ok=1 ;;
    *) relay_ok=0 ;;
  esac
  if [ "$relay_ok" -ne 1 ]; then
    printf 'install.sh: --relay: an origin such as https://relay.example.com (HTTP only on localhost)\n' >&2
    exit 2
  fi
fi
shell_kept=0
if [ -z "$allow_shell" ]; then
  allow_shell=0
  # The installed LaunchAgent's choice, or ghostyd's before the rename.
  for installed_plist in "$plist" "$old_plist"; do
    [ -f "$installed_plist" ] || continue
    if grep -q '<string>--allow-shell</string>' "$installed_plist"; then
      allow_shell=1
      shell_kept=1
    fi
    break
  done
fi
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/hesper-install.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT

# --- output and actions ------------------------------------------------------

show() {
  case $1 in
    "$HOME"/*) printf '~/%s' "${1#"$HOME"/}" ;;
    *) printf '%s' "$1" ;;
  esac
}
step() { printf '\n== %s\n' "$*"; }
ok() { printf '  ok: %s\n' "$*"; }
note() { printf '  note: %s\n' "$*"; }
warn() { printf '  warning: %s\n' "$*" >&2; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# act DESCRIPTION COMMAND [ARGS…]: says what it does, then does it (unless
# --dry-run, which only says it).
act() {
  description=$1
  shift
  if [ "$dry_run" -eq 1 ]; then
    printf '  would: %s\n' "$description"
  else
    printf '  + %s\n' "$description"
    "$@"
  fi
}

_backup_move() {
  mkdir -p "$(dirname -- "$2")"
  mv "$1" "$2"
  backup_count=$((backup_count + 1))
}
_backup_copy() {
  mkdir -p "$(dirname -- "$2")"
  cp -p "$1" "$2"
  backup_count=$((backup_count + 1))
}
backup_path() {
  case $1 in
    "$HOME"/*) printf '%s/%s' "$backup_root" "${1#"$HOME"/}" ;;
    *) printf '%s%s' "$backup_root" "$1" ;;
  esac
}
# Moves a file or link out of the way, into this run's backup directory.
backup_move() {
  act "move $(show "$1") to the backup $(show "$(backup_path "$1")")" _backup_move "$1" "$(backup_path "$1")"
}
backup_copy() {
  act "back up $(show "$1") to $(show "$(backup_path "$1")")" _backup_copy "$1" "$(backup_path "$1")"
}

_link() {
  mkdir -p "$(dirname -- "$2")"
  ln -s "$1" "$2"
}
# link_file SOURCE TARGET: TARGET becomes a symlink to SOURCE; whatever was
# there goes to the backup first.
link_file() {
  if [ -L "$2" ] && [ "$(readlink "$2")" = "$1" ]; then
    ok "$(show "$2") -> $(show "$1")"
    return
  fi
  if [ -e "$2" ] || [ -L "$2" ]; then
    backup_move "$2"
  fi
  act "link $(show "$2") -> $(show "$1")" _link "$1" "$2"
}

_write_file() {
  mkdir -p "$(dirname -- "$1")"
  printf '%s\n' "$2" > "$1.new"
  mv "$1.new" "$1"
}

agent_loaded() { "$LAUNCHCTL" print "gui/$uid/$1" >/dev/null 2>&1; }

# --- the rename: Ghosty -> Hesper ---------------------------------------------

# Hesper was called Ghosty: ghostyd ran as the LaunchAgent de.olezierau.ghostyd
# from ~/Applications/Ghosty.app, with its state in ~/.local/state/ghosty,
# machines.json in ~/.config/ghosty and the key helper in ~/.local/lib/ghosty.
# The agents, the history, the device keys (Secure Enclave blobs) and the
# relay enrollments are plain files in those directories, so moving them
# keeps every agent, session and pairing. Each step checks first: a second
# run, or a Mac that never had Ghosty, finds nothing to do.

_bootout_wait() {
  "$LAUNCHCTL" bootout "gui/$uid/$1" 2>/dev/null || true
  tries=0
  # bootout returns before the job is gone.
  while "$LAUNCHCTL" print "gui/$uid/$1" >/dev/null 2>&1 && [ "$tries" -lt 50 ]; do
    sleep 0.2
    tries=$((tries + 1))
  done
  if "$LAUNCHCTL" print "gui/$uid/$1" >/dev/null 2>&1; then
    die "$1 did not stop; stop it (launchctl bootout gui/$uid/$1), then run install.sh again"
  fi
}

ghosty_app_running() { "$PGREP" -x Ghosty >/dev/null 2>&1; }

_quit_ghosty_app() {
  "$OSASCRIPT" -e "tell application id \"$old_bundle_id\" to quit" >/dev/null 2>&1 || true
  tries=0
  while ghosty_app_running && [ "$tries" -lt 100 ]; do
    sleep 0.2
    tries=$((tries + 1))
  done
  if ghosty_app_running; then
    die 'Ghosty.app is still running: quit it, then run install.sh again'
  fi
}

_move_dir() {
  mkdir -p "$(dirname -- "$2")"
  mv "$1" "$2"
}

# migrate_dir OLD NEW: moves the directory OLD to NEW; true when it does (or,
# with --dry-run, would). Nothing when OLD is not a directory or a link
# (moved already); a NEW that exists too wins and OLD stays for a look.
migrate_dir() {
  if [ -L "$1" ] || [ ! -d "$1" ]; then
    return 1
  fi
  if [ -e "$2" ] || [ -L "$2" ]; then
    warn "$(show "$1") and $(show "$2") both exist: kept $(show "$2"), left $(show "$1") as it is (merge by hand)"
    return 1
  fi
  act "move $(show "$1") to $(show "$2")" _move_dir "$1" "$2"
}

# The paths saved before the rename (attachments of drafts and tasks) point
# into the old directories; JSON may escape the slashes.
_rewrite_paths() {
  A="$old_state_dir/" B="$state_dir/" C="$old_support/" D="$support/" perl -pi -e '
    BEGIN {
      for my $k (["A", "B"], ["C", "D"]) {
        my ($o, $n) = ($ENV{$k->[0]}, $ENV{$k->[1]});
        (my $eo = $o) =~ s{/}{\\/}g;
        (my $en = $n) =~ s{/}{\\/}g;
        push @p, [$o, $n], [$eo, $en];
      }
    }
    for my $p (@p) { s/\Q$p->[0]\E/$p->[1]/g }' "$1"
}
mentions_old_paths() {
  grep -qF -e "$old_state_dir/" -e "$old_support/" "$1" 2>/dev/null ||
    grep -qF -e "$(printf '%s/' "$old_state_dir" | sed 's|/|\\/|g')" -e "$(printf '%s/' "$old_support" | sed 's|/|\\/|g')" "$1" 2>/dev/null
}

migrate_from_ghosty() {
  step 'Ghosty is Hesper now (the rename)'
  migrated=0
  # The app first (a client of ghostyd), then the daemon; its agents stop
  # with it and resume under hesperd (launch_agent).
  if ghosty_app_running; then
    migrated=1
    act 'quit Ghosty.app' _quit_ghosty_app
  fi
  if agent_loaded "$old_label"; then
    migrated=1
    act "stop the LaunchAgent $old_label (ghostyd; its running agents resume under hesperd)" _bootout_wait "$old_label"
  fi
  if [ -e "$old_plist" ] || [ -L "$old_plist" ]; then
    migrated=1
    backup_move "$old_plist"
  fi
  # With --dry-run nothing moved: the follow-ups look where the files are.
  here_state=$state_dir here_lib=$lib_dir
  if migrate_dir "$old_state_dir" "$state_dir"; then
    migrated=1
    [ "$dry_run" -eq 0 ] || here_state=$old_state_dir
    # Paths saved before the rename keep working.
    act "link $(show "$old_state_dir") -> $(show "$state_dir")" _link "$state_dir" "$old_state_dir"
  fi
  if [ -f "$here_state/ghostyd.log" ] && [ ! -e "$here_state/hesperd.log" ]; then
    migrated=1
    act "rename $(show "$state_dir/ghostyd.log") to hesperd.log" mv "$state_dir/ghostyd.log" "$state_dir/hesperd.log"
  fi
  if [ -e "$here_state/ghostyd.sock" ] || [ -S "$here_state/ghostyd.sock" ]; then
    migrated=1
    act "remove the stale $(show "$state_dir/ghostyd.sock")" rm -f "$state_dir/ghostyd.sock"
  fi
  if migrate_dir "$old_support" "$support"; then
    migrated=1
    act "link $(show "$old_support") -> $(show "$support")" _link "$support" "$old_support"
  fi
  for file in "$here_state"/*.json "$here_state"/*.jsonl; do
    [ -f "$file" ] || continue
    if mentions_old_paths "$file"; then
      migrated=1
      act "rewrite the Ghosty paths in $(show "$state_dir/${file##*/}")" _rewrite_paths "$state_dir/${file##*/}"
    fi
  done
  if migrate_dir "$old_config_dir" "$config_dir"; then
    migrated=1
  fi
  if migrate_dir "$old_lib_dir" "$lib_dir"; then
    migrated=1
    [ "$dry_run" -eq 0 ] || here_lib=$old_lib_dir
  fi
  # The helper's old link (into Ghosty.app); link_tools adds hesper-keys.
  if [ -L "$here_lib/ghosty-keys" ]; then
    migrated=1
    act "remove the old link $(show "$lib_dir/ghosty-keys")" rm -f "$lib_dir/ghosty-keys"
  fi
  for name in ghostyd ghostyctl ghosty-keys; do
    if [ -L "$bin_dir/$name" ]; then
      migrated=1
      act "remove the old link $(show "$bin_dir/$name")" rm -f "$bin_dir/$name"
    elif [ -e "$bin_dir/$name" ]; then
      migrated=1
      backup_move "$bin_dir/$name"
    fi
  done
  if [ -d "$old_app" ]; then
    migrated=1
    old_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$old_app/Contents/Info.plist" 2>/dev/null || true)
    if [ "$old_id" = "$old_bundle_id" ]; then
      act "remove $(show "$old_app") (Hesper.app replaces it)" rm -rf "$old_app"
    else
      note "left alone: $(show "$old_app") (not Ghosty: ${old_id:-no bundle id})"
    fi
  fi
  # The app's settings (window layout, tile font, notifications…) live in
  # its defaults domain, which is named after the bundle id.
  if "$DEFAULTS" read "$old_bundle_id" >/dev/null 2>&1 && ! "$DEFAULTS" read "$bundle_id" >/dev/null 2>&1; then
    migrated=1
    act "copy Ghosty.app's settings ($old_bundle_id) to $bundle_id" _copy_defaults
  fi
  if [ "$migrated" -eq 0 ]; then
    ok 'nothing of Ghosty left to move'
  fi
}

_copy_defaults() {
  "$DEFAULTS" export "$old_bundle_id" "$tmp_dir/ghosty-defaults.plist"
  "$DEFAULTS" import "$bundle_id" "$tmp_dir/ghosty-defaults.plist"
}

# --- relay (part R) ------------------------------------------------------------

# hesperd serve runs both relay roles from the state directory's
# enrollments (docs/rebuild-contract.md, "Part R"): host.credentials.json
# serves this Mac's agents to devices approved here, controller.credentials.json
# reaches the other Macs. Both are found there by default; machine names come
# from the config directory (machines.json). Prints the extra <string>
# elements after `hesperd serve --state-dir DIR`.
relay_plist_args() {
  printf '    <string>--config-dir</string><string>%s</string>\n' "$config_dir"
  if [ "$allow_shell" -eq 1 ]; then
    printf '    <string>--allow-shell</string>\n'
  fi
}

# The relay is self-hosted and optional (relay/docs/deployment.md); Hesper
# has none built in. hesperctl login finds it in settings.json "relay" in
# the config directory (or --relay, $HESPER_RELAY, or the relay an existing
# enrollment names). hesperd uses the relay each credentials file records,
# so the setting never changes an enrolled role.
settings_file="$config_dir/settings.json"
# json_relay FILE: its "relay" string, or nothing. Uses plutil's exit status:
# some macOS versions print plutil's errors on stdout.
json_relay() {
  [ -f "$1" ] || return 0
  _v=$(plutil -extract relay raw -o - "$1" 2>/dev/null) && printf '%s' "$_v"
  return 0
}
settings_relay() { json_relay "$settings_file"; }
credentials_relay() { json_relay "$state_dir/$1.credentials.json"; } # ROLE
_set_relay() { # settings.json with "relay" set, the other settings kept
  cp -p "$settings_file" "$settings_file.new"
  plutil -replace relay -string "$relay" "$settings_file.new"
  mv "$settings_file.new" "$settings_file"
}
relay_step() {
  current=$(settings_relay)
  if [ -z "$relay" ]; then
    if [ -n "$current" ]; then ok "relay $current ($(show "$settings_file"))"; fi
    return 0
  fi
  if [ "$current" = "$relay" ]; then
    ok "relay $relay ($(show "$settings_file"))"
  elif [ -f "$settings_file" ]; then
    plutil -replace relay -string "$relay" -o /dev/null "$settings_file" >/dev/null 2>&1 ||
      die "$(show "$settings_file") is not valid JSON; fix it, or add \"relay\": \"$relay\" by hand"
    backup_copy "$settings_file"
    act "set relay to $relay in $(show "$settings_file")" _set_relay
  else
    act "write $(show "$settings_file"): relay $relay" _write_file "$settings_file" "{\"relay\": \"$relay\"}"
  fi
  for role in host controller; do
    enrolled=$(credentials_relay "$role")
    if [ -n "$enrolled" ] && [ "${enrolled%/}" != "$relay" ]; then
      note "the $role enrollment keeps its relay $enrolled; to move it to $relay, sign in again (remove its credentials file, then hesperctl login)"
    fi
  done
}
# relay_configured: hesperctl login would find a relay.
relay_configured() {
  [ -n "$relay" ] || [ -n "${HESPER_RELAY:-}" ] || [ -n "$(settings_relay)" ] ||
    [ -n "$(credentials_relay host)" ] || [ -n "$(credentials_relay controller)" ]
}
no_relay_hint() {
  note 'no relay configured: the agents on this Mac work without one.'
  printf '    To use agents across your Macs, deploy your own relay (%s),\n' "$(show "$repo_dir/relay/docs/deployment.md")"
  printf '    then run ./install.sh --relay https://relay.example.com (your relay'"'"'s address)\n'
  printf '    and sign in once per role: hesperctl login --role host|controller ...\n'
}

# relay_sign_in_hint ROLE…: the sign-in commands for the roles that have no
# enrollment yet (a browser approval each).
relay_sign_in_hint() {
  machine_name=$(scutil --get ComputerName 2>/dev/null || hostname)
  for role in "$@"; do
    suffix=''
    [ "$role" = controller ] && suffix=' controller'
    printf '    %s login --role %s --name "%s%s" --out %s\n' "$(show "$bin_dir/hesperctl")" "$role" "$machine_name" "$suffix" "$(show "$state_dir/$role.credentials.json")"
  done
  printf '    then restart hesperd: launchctl kickstart -k gui/%s/%s\n' "$uid" "$label"
}

launch_agent_plist() {
  cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key>
  <array>
    <string>$app/Contents/MacOS/hesperd</string>
    <string>serve</string>
    <string>--state-dir</string><string>$state_dir</string>
$(relay_plist_args)
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>$bin_dir:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$state_dir/hesperd.log</string>
  <key>StandardErrorPath</key><string>$state_dir/hesperd.log</string>
</dict>
</plist>
PLIST
}

if [ "$print_plist" -eq 1 ]; then
  launch_agent_plist
  exit 0
fi
if [ "$migrate_only" -eq 1 ]; then
  migrate_from_ghosty
  exit 0
fi

# --- steps -------------------------------------------------------------------

preflight() {
  [ "$(uname -s)" = Darwin ] || die 'Hesper runs on macOS only.'
  missing=''
  for command_name in make go swift git ditto shasum plutil; do
    command -v "$command_name" >/dev/null 2>&1 || missing="$missing $command_name"
  done
  if [ -n "$missing" ]; then
    if [ "$dry_run" -eq 1 ] || [ "$skip_build" -eq 1 ]; then
      warn "missing commands:$missing"
    else
      die "missing commands:$missing (Xcode 26 and Go: brew install go)"
    fi
  fi
  if [ -n "${NOTARY_PROFILE:-}" ] && { [ -z "${SIGN_IDENTITY:-}" ] || [ "$SIGN_IDENTITY" = - ]; }; then
    die 'NOTARY_PROFILE needs SIGN_IDENTITY (a Developer ID Application identity).'
  fi
  if [ -e "$app" ]; then
    installed_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app/Contents/Info.plist" 2>/dev/null || true)
    [ "$installed_id" = "$bundle_id" ] || die "$(show "$app") exists and is not Hesper ($bundle_id); move it away first."
  fi
}

build() {
  step 'Build'
  if [ "$skip_build" -eq 1 ]; then
    note 'build skipped (--skip-build)'
  else
    act 'build the relay tools: make -C relay build' make -C "$repo_dir/relay" build
    act 'build Hesper.app: make -C app build' make -C "$repo_dir/app" build
  fi
  if [ "$dry_run" -eq 0 ]; then
    for tool in $tools; do
      [ -x "$repo_dir/relay/dist/$tool" ] || die "relay/dist/$tool is missing: run make -C relay build"
    done
    [ -d "$staged" ] || die 'app/build/Hesper.app is missing: run make -C app build'
  fi
}

_embed() {
  cp -f "$repo_dir/relay/dist/$1" "$staged/Contents/MacOS/$1.new"
  chmod 755 "$staged/Contents/MacOS/$1.new"
  mv -f "$staged/Contents/MacOS/$1.new" "$staged/Contents/MacOS/$1"
}
_record_checksum() {
  shasum -a 256 "$repo_dir/relay/dist/hesperd" | cut -d' ' -f1 > "$staged/Contents/Resources/hesperd.sha256"
}

# hesperd, hesperctl and hesper-keys live in Hesper.app/Contents/MacOS: the
# app runs that hesperd for its tiles (part A), the LaunchAgent runs it as the
# daemon, and devicekey finds hesper-keys next to hesperctl.
bundle() {
  step 'Bundle the tools into Hesper.app'
  for tool in $tools; do
    act "embed relay/dist/$tool as Hesper.app/Contents/MacOS/$tool" _embed "$tool"
  done
  act "record hesperd's checksum (restart the daemon only when it changed)" _record_checksum
}

sign() {
  step 'Sign'
  entitlements="$repo_dir/packaging/hesperd.entitlements"
  if [ -n "${SIGN_IDENTITY:-}" ] && [ "$SIGN_IDENTITY" != - ]; then
    act "sign hesperd ($SIGN_IDENTITY, hardened runtime, timestamp)" \
      "$CODESIGN" --force --options runtime --timestamp --entitlements "$entitlements" --sign "$SIGN_IDENTITY" "$staged/Contents/MacOS/hesperd"
    for tool in hesperctl hesper-keys; do
      act "sign $tool ($SIGN_IDENTITY, hardened runtime, timestamp)" \
        "$CODESIGN" --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$staged/Contents/MacOS/$tool"
    done
    act "sign Hesper.app ($SIGN_IDENTITY, hardened runtime, timestamp)" \
      "$CODESIGN" --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$staged"
  else
    note 'SIGN_IDENTITY unset: ad-hoc signature (notifications and Gatekeeper need a Developer ID)'
    for tool in $tools; do
      act "sign $tool ad-hoc" "$CODESIGN" --force --sign - "$staged/Contents/MacOS/$tool"
    done
    act 'sign Hesper.app ad-hoc' "$CODESIGN" --force --sign - "$staged"
  fi
  act 'verify the signature' "$CODESIGN" --verify --strict --deep "$staged"
  if [ -n "${NOTARY_PROFILE:-}" ]; then
    zip="$repo_dir/app/build/Hesper-notarize.zip"
    act 'zip Hesper.app for notarization' /usr/bin/ditto -c -k --keepParent "$staged" "$zip"
    act "notarize (notarytool, keychain profile $NOTARY_PROFILE; waits for the result)" \
      "$XCRUN" notarytool submit "$zip" --keychain-profile "$NOTARY_PROFILE" --wait
    act 'staple the ticket to Hesper.app' "$XCRUN" stapler staple "$staged"
  fi
}

_install_app() {
  mkdir -p "$app_dir"
  rm -rf "$app.new"
  ditto "$staged" "$app.new"
  rm -rf "$app"
  mv "$app.new" "$app"
}

daemon_changed=1
install_app() {
  step "Install $(show "$app")"
  old_sum=$(cat "$app/Contents/Resources/hesperd.sha256" 2>/dev/null || true)
  new_sum=$(shasum -a 256 "$repo_dir/relay/dist/hesperd" 2>/dev/null | cut -d' ' -f1 || true)
  if [ -n "$old_sum" ] && [ "$old_sum" = "$new_sum" ]; then
    daemon_changed=0
  fi
  act "install Hesper.app to $(show "$app") (replaces the installed copy)" _install_app
}

link_tools() {
  step 'Links'
  link_file "$app/Contents/MacOS/hesperd" "$bin_dir/hesperd"
  link_file "$app/Contents/MacOS/hesperctl" "$bin_dir/hesperctl"
  # devicekey looks for hesper-keys here first (relay/pkg/devicekey).
  link_file "$app/Contents/MacOS/hesper-keys" "$lib_dir/hesper-keys"
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) note "$(show "$bin_dir") is not on PATH; add it in ~/.zprofile for hesperctl" ;;
  esac
}

_make_state_dir() {
  mkdir -p "$state_dir"
  chmod 700 "$state_dir"
}

state_and_credentials() {
  step 'State and relay sign-in'
  if [ -d "$state_dir" ] && [ "$(stat -f '%Lp' "$state_dir")" = 700 ]; then
    ok "$(show "$state_dir") (0700)"
  else
    act "create $(show "$state_dir") (0700)" _make_state_dir
  fi
  relay_step
  missing_roles=''
  for role in host controller; do
    if [ -f "$state_dir/$role.credentials.json" ]; then
      ok "reusing $(show "$state_dir/$role.credentials.json") ($role role)"
    else
      missing_roles="$missing_roles $role"
    fi
  done
  if [ -n "$missing_roles" ] && ! relay_configured; then
    no_relay_hint
    missing_roles=''
  fi
  case $missing_roles in
    '') ;;
    ' host controller')
      note 'no relay enrollments yet; local agents work without them. To reach your other Macs, sign in:'
      relay_sign_in_hint $missing_roles
      ;;
    *)
      note "no${missing_roles} enrollment yet (that role stays off); sign in:"
      relay_sign_in_hint $missing_roles
      ;;
  esac
  machines_step
  approval_hint
}

# machines.json (config directory) names machines by their host enrollment's
# device id: {"machines": {"<device id>": {"short": "L"}}}. A file that is
# there (yours, or Ghosty's moved over) is never changed.
machines_step() {
  machines_file="$config_dir/machines.json"
  host_id=''
  if [ -f "$state_dir/host.credentials.json" ]; then
    host_id=$(plutil -extract deviceId raw -o - "$state_dir/host.credentials.json" 2>/dev/null) || host_id=''
  fi
  if [ -f "$machines_file" ]; then
    if [ -n "$host_id" ] && short=$(plutil -extract "machines.$host_id.short" raw -o - "$machines_file" 2>/dev/null); then
      ok "$(show "$machines_file") kept as it is (this Mac is $short)"
      if [ -n "$machine_short" ] && [ "$machine_short" != "$short" ]; then
        note "--machine $machine_short not applied: machines.json already names this Mac $short"
      fi
    else
      ok "$(show "$machines_file") kept as it is"
      if [ -n "$host_id" ]; then
        note "it has no entry for this Mac's host device $host_id; to name it, add under \"machines\": \"$host_id\": {\"short\": \"${machine_short:-L}\"}"
      fi
    fi
  elif [ -n "$host_id" ] && [ -n "$machine_short" ]; then
    act "write $(show "$machines_file"): this Mac (host device $host_id) is $machine_short" \
      _write_file "$machines_file" "{\"machines\": {\"$host_id\": {\"short\": \"$machine_short\"}}}"
  elif [ -n "$host_id" ]; then
    note "no $(show "$machines_file"): your other Macs call this one by its host name; to name it, run again with --machine L (or M)"
  elif [ -n "$machine_short" ]; then
    note "--machine $machine_short not applied yet: it needs the host enrollment; run again after signing in"
  fi
}

# Device approval (part K): only for Macs whose approvals are not there yet.
approval_hint() {
  if [ -f "$state_dir/host.credentials.json" ] && [ ! -f "$state_dir/controllers.json" ]; then
    note 'no device approved on this Mac yet (controllers.json): until the first approval it accepts your controllers unauthenticated and starts no shells. Once hesperd runs on both Macs:'
    printf '    on the other Mac: hesperctl pair-host --machine %s --rights observe,answer,type,transfer[,shell] --wait 5m\n' "${machine_short:-<this Mac>}"
    printf '    here:             hesperctl approve <CODE it prints>\n'
  fi
  if [ -f "$state_dir/controller.credentials.json" ] && [ ! -f "$state_dir/trusted-hosts.json" ]; then
    note "this Mac's controller has not paired with another Mac yet (trusted-hosts.json). Once hesperd runs on both Macs:"
    printf '    here:             hesperctl pair-host --machine <other Mac> --rights observe,answer,type,transfer[,shell] --wait 5m\n'
    printf '    on the other Mac: hesperctl approve <CODE it prints>\n'
  fi
}

hooks_step() {
  step 'Claude and Codex hooks'
  bin="$bin_dir/hesperd"
  codex_home=${CODEX_HOME:-$HOME/.codex}
  planner=$bin
  if [ "$dry_run" -eq 1 ]; then
    planner="$repo_dir/relay/dist/hesperd"
    [ -x "$planner" ] || planner=$bin
  fi
  set -- --bin "$bin" --claude-settings "$HOME/.claude/settings.json" --codex-home "$codex_home"
  if [ ! -x "$planner" ]; then
    [ "$dry_run" -eq 1 ] || die "$(show "$bin") is missing"
    printf '  would: install hooks: hesperd hooks install %s\n' "$*"
    note 'no hesperd built yet, so the hook files were not checked'
    return
  fi
  "$planner" hooks install --dry-run "$@" > "$tmp_dir/hooks.json" || die 'hesperd hooks install --dry-run failed'
  changed=0
  i=0
  while path=$(plutil -extract "$i.path" raw -o - "$tmp_dir/hooks.json" 2>/dev/null); do
    if [ "$(plutil -extract "$i.changed" raw -o - "$tmp_dir/hooks.json")" = true ]; then
      changed=1
      if [ -f "$path" ]; then
        if grep -q 'ghostyd' "$path"; then
          note "$(show "$path"): replaces ghostyd's hooks (Hesper's former name)"
        fi
        backup_copy "$path"
      fi
      note "$(show "$path"): will be updated"
    else
      ok "$(show "$path")"
    fi
    if warning=$(plutil -extract "$i.warning" raw -o - "$tmp_dir/hooks.json" 2>/dev/null); then
      warn "$(show "$path"): $warning"
    fi
    i=$((i + 1))
  done
  if [ "$changed" -eq 1 ]; then
    act "install hooks: hesperd hooks install (keeps other hooks; also writes *.hesperd-backup)" "$bin" hooks install "$@"
  fi
}

# The Hesper skill (skills/hesper: SKILL.md and references/) teaches Claude
# Code and Codex to drive hesperctl. Both read Agent Skills from a folder per
# skill: Claude Code from ~/.claude/skills, Codex from ~/.agents/skills (its
# user-level location; it follows symlinks). Linked, not copied, so a pull
# updates it.
skills_step() {
  step 'Hesper skill for Claude Code and Codex'
  if [ "$skills" -eq 0 ]; then
    note 'skipped (--no-skills)'
    return
  fi
  skill="$repo_dir/skills/hesper"
  [ -f "$skill/SKILL.md" ] || die "$(show "$skill")/SKILL.md is missing"
  link_file "$skill" "$HOME/.claude/skills/hesper"
  link_file "$skill" "$HOME/.agents/skills/hesper"
}

_load_agent() {
  domain="gui/$uid"
  "$LAUNCHCTL" bootout "$domain/$label" 2>/dev/null || true
  tries=0
  # bootout returns before the job is gone; bootstrapping meanwhile fails.
  while "$LAUNCHCTL" print "$domain/$label" >/dev/null 2>&1 && [ "$tries" -lt 50 ]; do
    sleep 0.2
    tries=$((tries + 1))
  done
  "$LAUNCHCTL" bootstrap "$domain" "$plist"
}

launch_agent() {
  step "LaunchAgent $label (hesperd serve)"
  content=$(launch_agent_plist)
  plist_changed=0
  if [ -f "$plist" ] && [ "$(cat "$plist")" = "$content" ]; then
    ok "$(show "$plist")"
  else
    act "write $(show "$plist")" _write_file "$plist" "$content"
    plist_changed=1
  fi
  if [ "$plist_changed" -eq 1 ] || ! agent_loaded "$label"; then
    if [ "$shell_kept" -eq 1 ]; then
      note 'keeps --allow-shell from the installed LaunchAgent (--no-allow-shell turns it off)'
    fi
    act "(re)load $label; logs in $(show "$state_dir/hesperd.log")" _load_agent
    note 'a restarted hesperd resumes running agents with their sessions'
  elif [ "$daemon_changed" -eq 1 ]; then
    act 'restart hesperd for its new binary (running agents resume)' "$LAUNCHCTL" kickstart -k "gui/$uid/$label"
  else
    ok "$label running, hesperd unchanged"
  fi
}

_firewall_allow() {
  sudo_cmd=$SUDO
  [ -t 0 ] || sudo_cmd="$SUDO -n"
  if $sudo_cmd "$SOCKETFILTERFW" --add "$1" >/dev/null 2>&1 && $sudo_cmd "$SOCKETFILTERFW" --unblockapp "$1" >/dev/null 2>&1; then
    return 0
  fi
  printf '  Firewall not changed; other Macs use the relay. To connect directly:\n    sudo %s --add %s && sudo %s --unblockapp %s\n' \
    "$SOCKETFILTERFW" "$1" "$SOCKETFILTERFW" "$1" >&2
}

# The direct path (relay/docs/direct-path.md): with the macOS firewall on,
# hesperd accepts other Macs' direct connections only once it is allowed.
# A new build is a new binary, so every install checks again.
firewall_step() {
  step 'Firewall (direct path between your Macs)'
  if [ "$firewall" -eq 0 ]; then
    note 'skipped (--no-firewall)'
    return
  fi
  target="$app/Contents/MacOS/hesperd"
  if [ ! -x "$SOCKETFILTERFW" ] && ! command -v "$SOCKETFILTERFW" >/dev/null 2>&1; then
    note 'no application firewall'
    return
  fi
  if ! "$SOCKETFILTERFW" --getglobalstate 2>/dev/null | grep -q 'State = [12]'; then
    ok 'firewall off'
    return
  fi
  if "$SOCKETFILTERFW" --getblockall 2>/dev/null | grep -q 'set to enabled'; then
    note 'the firewall blocks all incoming connections: other Macs reach this one through the relay'
    return
  fi
  # ghostyd, Hesper's daemon before the rename, may still be listed.
  old_daemon=$("$SOCKETFILTERFW" --listapps 2>/dev/null | sed -n 's/^ *[0-9][0-9]* : *\(\/.*\/Ghosty\.app\/Contents\/MacOS\/ghostyd\)[[:space:]]*$/\1/p' | head -1)
  if [ -n "$old_daemon" ]; then
    note "the firewall still lists ghostyd ($(show "$old_daemon")); remove it by hand: sudo $SOCKETFILTERFW --remove $old_daemon"
  fi
  # Allowed already: its --listapps entry is followed by "( Allow incoming connections )".
  if "$SOCKETFILTERFW" --listapps 2>/dev/null | grep -A1 -F " : $target" | grep -q Allow; then
    ok "hesperd allowed through the firewall"
    return
  fi
  act "allow hesperd's incoming connections (sudo socketfilterfw --add/--unblockapp $(show "$target"))" _firewall_allow "$target"
}

login_item_step() {
  [ "$login_item" -eq 1 ] || return 0
  step 'Login item'
  act 'open Hesper.app at login (System Events login item, added once; replaces the Ghosty one)' "$OSASCRIPT" \
    -e "tell application \"System Events\" to if exists login item \"Ghosty\" then delete login item \"Ghosty\"" \
    -e "tell application \"System Events\" to if not (exists login item \"Hesper\") then make login item at end with properties {path:\"$app\", hidden:false}"
}

# --- MCP server (--mcp) -------------------------------------------------------

# Registers `hesperctl mcp` (Hesper's commands as MCP tools) with Claude
# Code, user scope (claude mcp add; ~/.claude.json backed up first), and
# with Codex ([mcp_servers.hesper] in $CODEX_HOME/config.toml, default
# ~/.codex; backed up first). A registration that already runs this
# hesperctl stays; one that runs another is replaced.
_codex_mcp_write() { # FILE CTL: drops an old [mcp_servers.hesper] table, appends ours
  mkdir -p "$(dirname -- "$1")"
  if [ -f "$1" ]; then
    awk '/^[[:space:]]*\[/ { skip = ($0 ~ /^[[:space:]]*\[mcp_servers\.hesper\][[:space:]]*(#.*)?$/) } !skip' "$1" > "$1.new"
  else
    : > "$1.new"
  fi
  printf '\n[mcp_servers.hesper]\ncommand = "%s"\nargs = ["mcp"]\n' "$2" >> "$1.new"
  mv "$1.new" "$1"
}
codex_mcp_current() { # FILE: the command line of [mcp_servers.hesper], if any
  awk '/^[[:space:]]*\[/ { t = ($0 ~ /^[[:space:]]*\[mcp_servers\.hesper\][[:space:]]*(#.*)?$/); next } t && /^[[:space:]]*command[[:space:]]*=/ { print }' "$1" 2>/dev/null
}
mcp_step() {
  [ "$mcp" -eq 1 ] || return 0
  step 'MCP server (hesperctl mcp)'
  ctl="$bin_dir/hesperctl"
  claude_json="$HOME/.claude.json"
  if ! command -v "$CLAUDE" >/dev/null 2>&1; then
    note "Claude Code (claude) not found; later: claude mcp add --scope user hesper -- $(show "$ctl") mcp"
  else
    current=''
    if [ -f "$claude_json" ]; then
      current=$(plutil -extract mcpServers.hesper.command raw -o - "$claude_json" 2>/dev/null) || current=''
      if [ -n "$current" ]; then
        arg=$(plutil -extract mcpServers.hesper.args.0 raw -o - "$claude_json" 2>/dev/null) || arg=''
        current="$current $arg"
      fi
    fi
    if [ "$current" = "$ctl mcp" ]; then
      ok "Claude Code: hesper runs $(show "$ctl") mcp"
    else
      if [ -f "$claude_json" ]; then
        backup_copy "$claude_json"
      fi
      if [ -n "$current" ]; then
        act "remove Claude Code's MCP server hesper ($current)" "$CLAUDE" mcp remove --scope user hesper
      fi
      act "register with Claude Code: claude mcp add --scope user hesper -- $(show "$ctl") mcp" \
        "$CLAUDE" mcp add --scope user hesper -- "$ctl" mcp
    fi
  fi
  codex_toml="${CODEX_HOME:-$HOME/.codex}/config.toml"
  if [ "$(codex_mcp_current "$codex_toml")" = "command = \"$ctl\"" ]; then
    ok "Codex: [mcp_servers.hesper] in $(show "$codex_toml") runs $(show "$ctl") mcp"
  elif [ ! -d "$(dirname -- "$codex_toml")" ] && ! command -v codex >/dev/null 2>&1; then
    note "Codex not found; later add [mcp_servers.hesper] command = \"$ctl\", args = [\"mcp\"] to $(show "$codex_toml")"
  else
    if [ -f "$codex_toml" ]; then
      backup_copy "$codex_toml"
    fi
    act "register with Codex: [mcp_servers.hesper] in $(show "$codex_toml")" _codex_mcp_write "$codex_toml" "$ctl"
  fi
}

finish() {
  step 'Done'
  if [ "$dry_run" -eq 1 ]; then
    printf '  Dry run: nothing was changed.\n'
  else
    printf '  Installed Hesper from %s\n' "$repo_dir"
    if [ "$backup_count" -gt 0 ]; then
      printf '  Moved or copied %s file(s) to %s\n' "$backup_count" "$(show "$backup_root")"
    fi
  fi
  cat <<EOF

  Next:
    open $(show "$app")
    Codex runs hooks only once trusted: open Codex and run /hooks once.
EOF
  if ! relay_configured; then
    cat <<EOF
    Optional, to use agents across your Macs: deploy your own relay
    ($(show "$repo_dir/relay/docs/deployment.md")), then run
    ./install.sh --relay https://relay.example.com (your relay's address)
    and hesperctl login once per role (host, controller).
EOF
  fi
}

preflight
build
bundle
sign
migrate_from_ghosty
install_app
link_tools
state_and_credentials
hooks_step
skills_step
launch_agent
firewall_step
login_item_step
mcp_step
finish
