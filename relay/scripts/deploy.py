#!/usr/bin/env python3
"""Deploy the relay independently alongside the existing Saphenion stack."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
REMOTE = "/opt/ghosty-relay"
SAPH = "/opt/saphenion"
DOMAIN = "relay.olezierau.de"


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=["deploy", "ps", "logs", "devices", "revoke", "reload-auth", "bootstrap-tls", "tls", "proxy", "health", "backup"])
    p.add_argument("value", nargs="?")
    p.add_argument("--server", default=os.getenv("HESPER_SERVER", "178.105.212.170"))
    p.add_argument("--user", default=os.getenv("HESPER_SSH_USER", "root"))
    a = p.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9.:-]+", a.server) or not re.fullmatch(r"[a-z_][a-z0-9_-]*", a.user):
        p.error("Invalid SSH destination")
    ssh = ["ssh", "-o", "IdentitiesOnly=no", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
           "-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/saph-ssh-%r@%h:%p", "-o", "ControlPersist=600"]
    destination = a.user + "@" + a.server

    def remote(command, **kwargs):
        return run([*ssh, destination, command], **kwargs)

    compose = f"cd {REMOTE}/deploy && docker compose -p ghosty-relay -f compose.shared.yaml"
    saph_compose = f"cd {SAPH} && docker compose --env-file .env.production"

    def upload(source, target):
        run(["rsync", "-az", "-e", shlex.join(ssh), str(source), destination + ":" + target])

    def proxy(bootstrap=False):
        source = "nginx.acme.conf" if bootstrap else "nginx.shared.conf"
        target = f"{SAPH}/nginx/conf.d/ghosty-relay.conf"
        guard = f"test ! -e {target} || {{ echo 'Relay proxy already exists; use just proxy'; exit 1; }}" if bootstrap else ":"
        remote(f'''set -eu
{guard}
backup=$(mktemp)
had_previous=0
if test -e {target}; then cp {target} "$backup"; had_previous=1; fi
cp {REMOTE}/deploy/{source} {target}
if docker exec saphenion-nginx-1 nginx -t; then
  docker exec saphenion-nginx-1 nginx -s reload
  rm "$backup"
else
  if test "$had_previous" = 1; then cp "$backup" {target}; else rm {target}; fi
  rm "$backup"
  exit 1
fi''')

    if a.action == "deploy":
        cfg = ROOT / "deploy/auth.json"
        secret = ROOT / "deploy/secrets/github-client-secret"
        config = json.loads(cfg.read_text())
        if config.get("publicURL") != "https://" + DOMAIN:
            p.error("Deployment publicURL does not match relay.olezierau.de")
        if secret.stat().st_mode & 0o077 or not secret.read_bytes().strip():
            p.error("Client secret must be nonempty and mode 0600 or 0400")
        runtime = ROOT / ".state/production"
        (runtime / "bin").mkdir(parents=True, exist_ok=True)
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
        # Deploy names: the image keeps the binaries ghosty-relay and ghostyctl
        # (deploy/Dockerfile.prebuilt) until the next relay deploy.
        for binary, package in [("ghosty-relay", "hesper-relay"), ("ghostyctl", "hesperctl")]:
            run(["go", "build", "-trimpath", "-o", str(runtime / "bin" / binary), "./cmd/" + package], cwd=ROOT, env=env)
        (runtime / "Dockerfile").write_bytes((ROOT / "deploy/Dockerfile.prebuilt").read_bytes())
        digest = hashlib.sha256()
        for f in [runtime / "Dockerfile", *sorted((runtime / "bin").iterdir())]:
            digest.update(f.read_bytes())
        image = "ghosty-relay:" + digest.hexdigest()[:16]
        remote(f"install -d -m 0700 {REMOTE} {REMOTE}/deploy {REMOTE}/deploy/secrets {REMOTE}/.state/production; docker network inspect saphenion_default >/dev/null")
        upload(str(runtime) + "/", REMOTE + "/.state/production/")
        for name in ["compose.shared.yaml", "nginx.acme.conf", "nginx.shared.conf", "auth.json"]:
            upload(ROOT / "deploy" / name, REMOTE + "/deploy/" + name)
        upload(secret, REMOTE + "/deploy/secrets/github-client-secret")
        remote(f"chown 10001:10001 {REMOTE}/deploy/auth.json {REMOTE}/deploy/secrets/github-client-secret; chmod 0400 {REMOTE}/deploy/auth.json {REMOTE}/deploy/secrets/github-client-secret")
        remote(f"printf '%s\\n' {shlex.quote('GHOSTY_IMAGE=' + image)} > {REMOTE}/deploy/.env")
        remote(compose + " build relay && " + compose + " up -d --no-deps --force-recreate relay")
        remote(compose + " ps")
        remote("for attempt in 1 2 3 4 5; do " + compose + " exec -T relay wget -q -T 5 -O- http://127.0.0.1:8787/healthz && exit 0; sleep 1; done; exit 1")
    elif a.action == "bootstrap-tls":
        proxy(bootstrap=True)
    elif a.action == "tls":
        # Reuse the existing ACME account and renewal volumes. No Saphenion restart.
        remote(saph_compose + " run --rm --no-deps --entrypoint certbot certbot certonly --non-interactive --webroot -w /var/www/certbot --cert-name " + DOMAIN + " -d " + DOMAIN + " --keep-until-expiring")
        proxy()
    elif a.action == "proxy":
        for name in ["nginx.acme.conf", "nginx.shared.conf"]:
            upload(ROOT / "deploy" / name, REMOTE + "/deploy/" + name)
        proxy()
    elif a.action == "ps":
        remote(compose + " ps")
    elif a.action == "logs":
        remote(compose + " logs --tail=100 -f relay")
    elif a.action == "devices":
        remote(compose + " exec -T relay ghostyctl devices --admin-socket /data/admin.sock")
    elif a.action == "revoke":
        if not a.value or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", a.value):
            p.error("A valid device ID is required")
        remote(compose + " exec -T relay ghostyctl revoke --admin-socket /data/admin.sock --device " + shlex.quote(a.value))
    elif a.action == "reload-auth":
        # Copy through the bound inode, so SIGHUP sees an updated file.
        cfg = (ROOT / "deploy/auth.json").read_bytes()
        json.loads(cfg)
        remote(f"cat > {REMOTE}/deploy/auth.json && chmod 0400 {REMOTE}/deploy/auth.json", input=cfg)
        remote(compose + " kill -s SIGHUP relay")
    elif a.action == "health":
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", "https://" + DOMAIN + "/healthz"])
        print()
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", "-o", "/dev/null", "-w", "Saphenion HTTP %{http_code}\\n", "https://saphenion.olezierau.de/"])
    elif a.action == "backup":
        # Pause writes to get a bbolt-consistent copy; always resume in finally.
        backup = ROOT / ".state/backups" / (time.strftime("%Y%m%d-%H%M%S") + ".db")
        backup.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        remote(compose + " pause relay")
        try:
            with backup.open("xb") as f:
                os.chmod(backup, 0o600)
                remote("cat /var/lib/docker/volumes/ghosty-relay_relay-data/_data/relay.db", stdout=f)
        finally:
            remote(compose + " unpause relay")
        print("Saved", backup)


if __name__ == "__main__":
    try:
        main()
    except (subprocess.CalledProcessError, OSError, ValueError) as error:
        print("Deployment command failed:", error, file=sys.stderr)
        sys.exit(1)
