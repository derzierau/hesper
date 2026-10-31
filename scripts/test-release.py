#!/usr/bin/env python3
"""Exercise cask generation and binary setup with no real daemon changes."""
import hashlib
import os
import plistlib
import shutil
import subprocess
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix='hesper-release-test-') as temporary:
    temp = Path(temporary)
    checkout = temp/'repo'
    (checkout/'scripts').mkdir(parents=True)
    generator = checkout/'scripts/update-homebrew.py'
    shutil.copy(root/'scripts/update-homebrew.py', generator)
    assets = temp/'assets'
    assets.mkdir()
    for arch in ('arm64', 'x86_64'):
        (assets/f'Hesper-v1.2.3-{arch}.zip').write_bytes(arch.encode())
    subprocess.run(['python3', str(generator), 'v1.2.3', str(assets)], check=True)
    cask = (checkout/'Casks/hesper.rb').read_text()
    for arch in ('arm64', 'x86_64'):
        assert hashlib.sha256(arch.encode()).hexdigest() in cask
    subprocess.run(['ruby', '-c', str(checkout/'Casks/hesper.rb')], check=True)
    for tag in ('main', 'v1.2.2'):
        result = subprocess.run(['python3', str(generator), tag, str(assets)], capture_output=True)
        assert result.returncode != 0
    assert 'github.com/derzierau/hesper/releases/download/v#{version}' in cask
    assert 'Authorization' not in cask and 'HOMEBREW_GITHUB_API_TOKEN' not in cask
    assert 'container type: :zip' in cask
    # A missing architecture must fail before publishing a new cask.
    (assets/'Hesper-v1.2.3-x86_64.zip').unlink()
    assert subprocess.run(['python3', str(generator), 'v1.2.3', str(assets)], capture_output=True).returncode != 0
    assert (checkout/'Casks/hesper.rb').read_text() == cask
    # hesperd reports the release version, not "dev" (the release passes it).
    build = subprocess.run(['make', '-n', '-C', str(root/'relay'), 'build', 'VERSION=1.2.3'],
                           capture_output=True, text=True, check=True).stdout
    assert "-X main.version=1.2.3" in build
    assert 'make -C relay build VERSION="${RELEASE_TAG#v}"' in (root/'.github/workflows/release.yml').read_text()
    if os.uname().sysname == 'Darwin':
        app = temp/'App & Space/Hesper.app'
        resources = app/'Contents/Resources'
        resources.mkdir(parents=True)
        (resources/'hesper-skill').mkdir()
        (resources/'hesper-skill/SKILL.md').write_text('test')
        shutil.copy(root/'packaging/hesper-setup.sh', resources/'hesper-setup.sh')
        tools = app/'Contents/MacOS'
        tools.mkdir()
        stub = '#!/bin/sh\nprintf "%s\\n" "$0 $*" >> "$HESPER_TEST_LOG"\n'
        for tool in ('hesperd', 'hesperctl', 'hesper-keys'):
            (tools/tool).write_text(stub)
            (tools/tool).chmod(0o755)
        stubs = temp/'stubs'
        stubs.mkdir()
        (stubs/'codesign').write_text(stub)
        (stubs/'codesign').chmod(0o755)
        # launchctl bootout returns before the old daemon is gone: print still
        # finds it twice, and bootstrap fails meanwhile, as launchd does.
        (stubs/'launchctl').write_text(stub + '''state="$HESPER_TEST_LOG.loaded"
case "$1" in
  bootout) echo 2 > "$state" ;;
  print) n=$(cat "$state" 2>/dev/null || echo 0); [ "$n" -gt 0 ] || exit 113; echo $((n - 1)) > "$state" ;;
  bootstrap) [ "$(cat "$state" 2>/dev/null || echo 0)" -eq 0 ] || { echo "Bootstrap failed: 5: Input/output error" >&2; exit 5; } ;;
esac
''')
        (stubs/'launchctl').chmod(0o755)
        home = temp/'home & space'
        home.mkdir()
        env = dict(os.environ, HOME=str(home), PATH=f'{stubs}:/usr/bin:/bin', HESPER_TEST_LOG=str(temp/'calls'))
        before = {str(p): p.read_bytes() for p in app.rglob('*') if p.is_file()}
        subprocess.run(['/bin/sh', str(resources/'hesper-setup.sh')], env=env, check=True)
        plist = home/'Library/LaunchAgents/de.olezierau.hesperd.plist'
        value = plistlib.loads(plist.read_bytes())
        assert value['ProgramArguments'] == [str(tools/'hesperd'), 'serve']
        assert (home/'.agents/skills/hesper').resolve() == (resources/'hesper-skill').resolve()
        flags = ['--allow-shell', '--allow-forward', '3000,8000-8999']
        value['ProgramArguments'] += flags
        plist.write_bytes(plistlib.dumps(value))
        subprocess.run(['/bin/sh', str(resources/'hesper-setup.sh')], env=env, check=True)
        assert plistlib.loads(plist.read_bytes())['ProgramArguments'] == [str(tools/'hesperd'), 'serve'] + flags
        # brew upgrade's uninstall step deletes the plist; setup keeps the flags.
        plist.unlink()
        subprocess.run(['/bin/sh', str(resources/'hesper-setup.sh')], env=env, check=True)
        assert plistlib.loads(plist.read_bytes())['ProgramArguments'] == [str(tools/'hesperd'), 'serve'] + flags
        assert before == {str(p): p.read_bytes() for p in app.rglob('*') if p.is_file()}
        assert 'hooks install --bin' in (temp/'calls').read_text()
print('Release packaging checks passed')
