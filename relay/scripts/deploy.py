#!/usr/bin/env python3
"""Deploy the relay to a server that already runs an nginx + certbot stack.

The relay joins that stack's Docker network; its nginx terminates HTTPS and
its certbot obtains and renews the relay's certificate. No host port is
published. Everything specific to your server comes from the environment or
from relay/deploy/local.env (KEY=VALUE lines, ignored by Git; the environment
wins). See docs/deployment.md, "Deploy next to an existing nginx".

Required:
  RELAY_DOMAIN            the relay's public name, e.g. relay.example.com
  RELAY_SERVER            SSH host of the server (name or address)
  RELAY_PROXY_NETWORK     Docker network of the nginx container
  RELAY_PROXY_NGINX       name of the nginx container (nginx -t / -s reload)
  RELAY_PROXY_DIR         directory of the nginx/certbot Compose project
                          (only for bootstrap-tls, tls and proxy)
Optional:
  RELAY_SSH_USER          SSH user (default root)
  RELAY_REMOTE_DIR        where the relay's files go (default /opt/hesper-relay)
  RELAY_COMPOSE_PROJECT   Compose project name (default hesper-relay); its
                          volume <project>_relay-data holds the database
  RELAY_PROXY_CONF_DIR    nginx conf.d directory (default
                          $RELAY_PROXY_DIR/nginx/conf.d)
  RELAY_PROXY_CONF_NAME   file name of the relay's vhost (default hesper-relay.conf)
  RELAY_PROXY_COMPOSE_ARGS  extra arguments for that project's docker compose,
                          e.g. "--env-file .env.production"
  RELAY_PROXY_CERTBOT     certbot service in that project (default certbot)
"""
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
LOCAL_ENV = ROOT / "deploy/local.env"
TEMPLATES = ["nginx.acme.conf.template", "nginx.shared.conf.template"]


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def load_settings():
    """relay/deploy/local.env under the environment."""
    settings = {}
    if LOCAL_ENV.exists():
        for number, line in enumerate(LOCAL_ENV.read_text().splitlines(), 1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            key, sep, value = line.partition("=")
            if not sep:
                raise ValueError(f"{LOCAL_ENV}:{number}: expected KEY=VALUE")
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            settings[key.strip().removeprefix("export ").strip()] = value
    settings.update({k: v for k, v in os.environ.items() if k.startswith("RELAY_")})
    return settings


def render(template, domain):
    """A deploy/*.conf.template with ${RELAY_DOMAIN} filled in (as
    envsubst '${RELAY_DOMAIN}' would; nginx's own $variables stay)."""
    return (ROOT / "deploy" / template).read_text().replace("${RELAY_DOMAIN}", domain)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("action", choices=["deploy", "ps", "logs", "devices", "revoke", "reload-auth", "bootstrap-tls", "tls", "proxy", "health", "backup"])
    p.add_argument("value", nargs="?")
    p.add_argument("--server", help="SSH host (default $RELAY_SERVER)")
    p.add_argument("--user", help="SSH user (default $RELAY_SSH_USER or root)")
    p.add_argument("--domain", help="relay domain (default $RELAY_DOMAIN)")
    a = p.parse_args()
    s = load_settings()

    def need(key, flag=None):
        value = (getattr(a, flag) if flag else None) or s.get(key, "")
        if not value:
            p.error(f"{key} is not set: export it or add {key}=… to {LOCAL_ENV.relative_to(ROOT.parent)}")
        return value

    domain = need("RELAY_DOMAIN", "domain")
    if not re.fullmatch(r"[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+", domain):
        p.error("RELAY_DOMAIN must be a host name such as relay.example.com")
    remote_dir = s.get("RELAY_REMOTE_DIR") or "/opt/hesper-relay"
    project = s.get("RELAY_COMPOSE_PROJECT") or "hesper-relay"
    for name, value in [("RELAY_REMOTE_DIR", remote_dir), ("RELAY_COMPOSE_PROJECT", project)]:
        if not re.fullmatch(r"[A-Za-z0-9_./-]+", value):
            p.error(f"Invalid {name}")

    def ssh_destination():
        server = need("RELAY_SERVER", "server")
        user = a.user or s.get("RELAY_SSH_USER") or "root"
        if not re.fullmatch(r"[A-Za-z0-9.:-]+", server) or not re.fullmatch(r"[a-z_][a-z0-9_-]*", user):
            p.error("Invalid SSH destination")
        return user + "@" + server

    if a.action == "health":
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", "https://" + domain + "/healthz"])
        print()
        return

    destination = ssh_destination()
    ssh = ["ssh", "-o", "IdentitiesOnly=no", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
           "-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/hesper-relay-ssh-%r@%h:%p", "-o", "ControlPersist=600"]

    def remote(command, **kwargs):
        return run([*ssh, destination, command], **kwargs)

    def upload(source, target):
        run(["rsync", "-az", "-e", shlex.join(ssh), str(source), destination + ":" + target])

    compose = f"cd {remote_dir}/deploy && docker compose -p {shlex.quote(project)} -f compose.shared.yaml"
    runtime = ROOT / ".state/production"

    def upload_proxy_confs():
        rendered = runtime / "nginx"
        rendered.mkdir(parents=True, exist_ok=True)
        for template in TEMPLATES:
            out = rendered / template.removesuffix(".template")
            out.write_text(render(template, domain))
            upload(out, f"{remote_dir}/deploy/{out.name}")

    def proxy(bootstrap=False):
        nginx = need("RELAY_PROXY_NGINX")
        conf_dir = s.get("RELAY_PROXY_CONF_DIR") or need("RELAY_PROXY_DIR") + "/nginx/conf.d"
        target = shlex.quote(conf_dir + "/" + (s.get("RELAY_PROXY_CONF_NAME") or "hesper-relay.conf"))
        source = "nginx.acme.conf" if bootstrap else "nginx.shared.conf"
        guard = f"test ! -e {target} || {{ echo 'Relay proxy already exists; use just proxy'; exit 1; }}" if bootstrap else ":"
        remote(f'''set -eu
{guard}
backup=$(mktemp)
had_previous=0
if test -e {target}; then cp {target} "$backup"; had_previous=1; fi
cp {remote_dir}/deploy/{source} {target}
if docker exec {shlex.quote(nginx)} nginx -t; then
  docker exec {shlex.quote(nginx)} nginx -s reload
  rm "$backup"
else
  if test "$had_previous" = 1; then cp "$backup" {target}; else rm {target}; fi
  rm "$backup"
  exit 1
fi''')

    if a.action == "deploy":
        network = need("RELAY_PROXY_NETWORK")
        cfg = ROOT / "deploy/auth.json"
        secret = ROOT / "deploy/secrets/github-client-secret"
        config = json.loads(cfg.read_text())
        if config.get("publicURL") != "https://" + domain:
            p.error(f"deploy/auth.json publicURL does not match https://{domain}")
        if secret.stat().st_mode & 0o077 or not secret.read_bytes().strip():
            p.error("Client secret must be nonempty and mode 0600 or 0400")
        (runtime / "bin").mkdir(parents=True, exist_ok=True)
        env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
        # The image names the binaries ghosty-relay and ghostyctl (Hesper's
        # former name; deploy/Dockerfile.prebuilt).
        for binary, package in [("ghosty-relay", "hesper-relay"), ("ghostyctl", "hesperctl")]:
            run(["go", "build", "-trimpath", "-o", str(runtime / "bin" / binary), "./cmd/" + package], cwd=ROOT, env=env)
        (runtime / "Dockerfile").write_bytes((ROOT / "deploy/Dockerfile.prebuilt").read_bytes())
        digest = hashlib.sha256()
        for f in [runtime / "Dockerfile", *sorted((runtime / "bin").iterdir())]:
            digest.update(f.read_bytes())
        image = "ghosty-relay:" + digest.hexdigest()[:16]
        remote(f"install -d -m 0700 {remote_dir} {remote_dir}/deploy {remote_dir}/deploy/secrets {remote_dir}/.state/production; docker network inspect {shlex.quote(network)} >/dev/null")
        upload(str(runtime / "bin") + "/", remote_dir + "/.state/production/bin/")
        upload(runtime / "Dockerfile", remote_dir + "/.state/production/Dockerfile")
        for name in ["compose.shared.yaml", "auth.json"]:
            upload(ROOT / "deploy" / name, remote_dir + "/deploy/" + name)
        upload_proxy_confs()
        upload(secret, remote_dir + "/deploy/secrets/github-client-secret")
        remote(f"chown 10001:10001 {remote_dir}/deploy/auth.json {remote_dir}/deploy/secrets/github-client-secret; chmod 0400 {remote_dir}/deploy/auth.json {remote_dir}/deploy/secrets/github-client-secret")
        dotenv = "".join(f"{k}={v}\n" for k, v in [("GHOSTY_IMAGE", image), ("RELAY_PROXY_NETWORK", network)])
        remote(f"cat > {remote_dir}/deploy/.env", input=dotenv.encode())
        remote(compose + " build relay && " + compose + " up -d --no-deps --force-recreate relay")
        remote(compose + " ps")
        remote("for attempt in 1 2 3 4 5; do " + compose + " exec -T relay wget -q -T 5 -O- http://127.0.0.1:8787/healthz && exit 0; sleep 1; done; exit 1")
    elif a.action == "bootstrap-tls":
        upload_proxy_confs()
        proxy(bootstrap=True)
    elif a.action == "tls":
        # Uses the existing stack's certbot (its ACME account and renewal);
        # nothing else in that stack restarts.
        proxy_compose = f"cd {shlex.quote(need('RELAY_PROXY_DIR'))} && docker compose {s.get('RELAY_PROXY_COMPOSE_ARGS', '')}"
        certbot = shlex.quote(s.get("RELAY_PROXY_CERTBOT") or "certbot")
        remote(proxy_compose + f" run --rm --no-deps --entrypoint certbot {certbot} certonly --non-interactive --webroot -w /var/www/certbot --cert-name {domain} -d {domain} --keep-until-expiring")
        proxy()
    elif a.action == "proxy":
        upload_proxy_confs()
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
        remote(f"cat > {remote_dir}/deploy/auth.json && chmod 0400 {remote_dir}/deploy/auth.json", input=cfg)
        remote(compose + " kill -s SIGHUP relay")
    elif a.action == "backup":
        # Pause writes to get a bbolt-consistent copy; always resume in finally.
        backup = ROOT / ".state/backups" / (time.strftime("%Y%m%d-%H%M%S") + ".db")
        backup.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        remote(compose + " pause relay")
        try:
            with backup.open("xb") as f:
                os.chmod(backup, 0o600)
                remote(f"cat /var/lib/docker/volumes/{project}_relay-data/_data/relay.db", stdout=f)
        finally:
            remote(compose + " unpause relay")
        print("Saved", backup)


if __name__ == "__main__":
    try:
        main()
    except (subprocess.CalledProcessError, OSError, ValueError) as error:
        print("Deployment command failed:", error, file=sys.stderr)
        sys.exit(1)
