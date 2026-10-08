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
    # A missing architecture must fail before publishing a new cask.
    (assets/'Hesper-v1.2.3-x86_64.zip').unlink()
    assert subprocess.run(['python3', str(generator), 'v1.2.3', str(assets)], capture_output=True).returncode != 0
    assert (checkout/'Casks/hesper.rb').read_text() == cask
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
        for tool in ('codesign', 'launchctl'):
            (stubs/tool).write_text(stub)
            (stubs/tool).chmod(0o755)
        home = temp/'home & space'
        home.mkdir()
        env = dict(os.environ, HOME=str(home), PATH=f'{stubs}:/usr/bin:/bin', HESPER_TEST_LOG=str(temp/'calls'))
        before = {str(p): p.read_bytes() for p in app.rglob('*') if p.is_file()}
        subprocess.run(['/bin/sh', str(resources/'hesper-setup.sh')], env=env, check=True)
        plist = home/'Library/LaunchAgents/de.olezierau.hesperd.plist'
        value = plistlib.loads(plist.read_bytes())
        assert value['ProgramArguments'] == [str(tools/'hesperd'), 'serve']
        assert (home/'.agents/skills/hesper').resolve() == (resources/'hesper-skill').resolve()
        value['ProgramArguments'].append('--allow-shell')
        plist.write_bytes(plistlib.dumps(value))
        subprocess.run(['/bin/sh', str(resources/'hesper-setup.sh')], env=env, check=True)
        assert '--allow-shell' in plistlib.loads(plist.read_bytes())['ProgramArguments']
        assert before == {str(p): p.read_bytes() for p in app.rglob('*') if p.is_file()}
        assert 'hooks install --bin' in (temp/'calls').read_text()
print('Release packaging checks passed')
