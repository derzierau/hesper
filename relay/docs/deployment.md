# Deploy relay.olezierau.de

## Existing Hetzner server (production)

The relay shares `178.105.212.170` with Saphenion. Its separate Compose project
lives in `/opt/ghosty-relay`, with its own persistent volume. It joins
`saphenion_default`; the existing nginx and certbot provide HTTPS. Port 8787 is
not published. The container runs as UID 10001, with a read-only root filesystem,
256 MB memory and 0.5 CPU limits.

Run from your local checkout (requires Go, Python 3, just, rsync, and SSH access
through your agent; the server host key must already be trusted):

```sh
cd ~/projects/hesper/relay
just deploy      # build Linux binaries locally, upload, recreate only the relay
just health      # check public relay health and Saphenion
just ps
just logs
```

Local `deploy/auth.json` and `deploy/secrets/github-client-secret` are ignored by
Git. The secret must be mode 0600 or 0400. Deployment copies both over SSH and
sets remote ownership to UID 10001 and mode 0400. `HESPER_SERVER` and
`HESPER_SSH_USER` override the SSH destination; the shared stack paths and domain
are deliberately specific to this installation.

For first-time HTTPS setup, create the DNS A record `relay -> 178.105.212.170`,
then run after `just deploy`:

```sh
just bootstrap-tls  # install HTTP-only ACME virtual host once
just tls            # obtain certificate using the existing ACME account
```

`just proxy` updates and validates only the relay virtual host, then reloads
nginx. Existing certbot renewal and nginx's periodic reload cover this certificate.
Saphenion's local justfile excludes `/nginx/conf.d/ghosty-relay.conf` from its
rsync deployment so it preserves the relay-owned configuration.

```sh
just devices
just revoke DEVICE_ID
just reload-auth    # apply allowedGitHubIds changes only
just backup         # briefly pause writes, save protected .state/backups/*.db
```

Deployments briefly disconnect relay clients, which reconnect automatically.
Configuration/secret changes other than the allowlist require `just deploy`.
Backups contain credentials and must stay private. The relay remains a single
instance; do not share its bbolt database between replicas.

## GitHub OAuth App

Register an OAuth App in GitHub developer settings:
https://github.com/settings/applications/new

- Application name: `Hesper Relay` (any name works)
- Homepage URL: `https://relay.olezierau.de`
- Authorization callback URL: `https://relay.olezierau.de/auth/github/callback`
- No repository scopes or GitHub device-flow setting are required.

Record the client ID in the config. Save the client secret directly into a
protected file on the server. Do not put it in chat, shell arguments, images,
source control, or a Compose environment variable. The relay exchanges GitHub
codes server-side with PKCE and discards the GitHub access token after identity
lookup. Hosts and controllers receive independent relay credentials.

The template's `allowedGitHubIds` holds the placeholder `12345678`. Replace it
with the numeric GitHub IDs allowed to sign in (look one up with
`curl -s https://api.github.com/users/<login>` and read `id`). Device ownership
uses `github:<id>`, so a username rename cannot transfer access.

## Alternative: standalone server (Caddy)

Point the DNS A record (and AAAA only if IPv6 works on the server) for
`relay.olezierau.de` to the server. Permit inbound TCP 80 and 443. Hosts need only
outbound HTTPS. Hosts do not need Tailscale.

Copy this repository to the server, install Docker with Compose, then from
`relay/deploy`:

```sh
cp auth.example.json auth.json
mkdir -m 0700 secrets
```

Replace `githubClientId` in `auth.json` with the real ID. Keep
`githubSecretFile` set to `/run/secrets/github-client-secret`. Install the secret
file at `deploy/secrets/github-client-secret`, owned by UID/GID `10001:10001`, mode
`0400` or `0600` so the non-root relay can read it. For an existing protected file:

```sh
sudo install -o 10001 -g 10001 -m 0400 /protected/path/github-client-secret secrets/github-client-secret

docker compose config --quiet
docker compose up -d --build
curl --fail https://relay.olezierau.de/healthz
```

The health response reports protocol 2. It verifies process reachability, not the
GitHub credentials; complete one browser login to verify OAuth configuration.
`auth.json` and `secrets/` are excluded from Git and the Docker build context.
The database is in `relay-data`; Caddy certificates are in `caddy-data`.

## Enroll each device

Each Mac runs one `hesperd` with two enrollments (rebuild contract part R):
the host role serves its agents, the controller role reaches the other
Macs' agents. Build with `make build`, then on each Mac:

```sh
./dist/hesperctl login --relay https://relay.olezierau.de \
  --name "Mac mini" --role host --out ~/.local/state/hesper/host.credentials.json
./dist/hesperctl login --relay https://relay.olezierau.de \
  --name "Mac mini" --role controller --out ~/.local/state/hesper/controller.credentials.json
./dist/hesperd serve        # under its LaunchAgent (part C)
```

Open the URL printed by login, sign in to GitHub, compare the displayed code with
the CLI, and approve the named device. Each device needs its own credentials
files. Then approve each Mac's controller on the other Mac (`hesperctl
pair-host --machine <other>` on one, `hesperctl approve CODE` on the other;
see the README).

A native app that signs in through its own callback URL needs that exact URL in
`appCallbacks`. The default empty list supports CLI browser approval.

## Renewal and access changes

Access tokens last 15 minutes. The host reconnects and renews automatically; CLI
commands renew as needed and `watch` reconnects. Refresh families expire after
30 days, requiring another browser login. Credentials are written atomically with
mode 0600; a persistent adjacent `.lock` serializes processes using the same file.
Avoid sharing a host credential across processes or machines.

Clients replace the credentials file only after a renewed pair is durably saved,
and discard nothing unless the relay explicitly rejects the refresh credential
(HTTP 401 `unauthorized` or 403 `not_allowed`). Network errors, timeouts, 429,
5xx and malformed answers keep the stored credential; both roles of
`hesperd` keep running and retry with backoff. Each refresh is single
use, with one exception for a lost response: within 60 seconds of a rotation,
the device that holds the access token issued with the consumed refresh
credential (sent as `Authorization: Bearer`) gets the same successor pair again,
as long as that successor is unused. The relay keeps that pair only encrypted
under the consumed credential. Any other reuse revokes the device.

After a rejection that role of hesperd stops (the other and the agents keep
running) and logs the command to run, for example
`rm PATH && hesperctl login --relay URL --role host --name "$(hostname -s)" --out PATH`.

To change permitted users, edit `allowedGitHubIds` in `auth.json` and send SIGHUP:

```sh
docker compose kill -s SIGHUP relay
```

Removing an ID closes that user's active connections and blocks new authentication
and refresh. An empty list denies everyone. Re-adding a user restores access for
still-valid, nonrevoked credentials. To invalidate those permanently, revoke devices.
Only allowlist changes reload; restart for client ID, secret, URL, or callback changes.
With a single-file bind mount, edit the existing file in place; replacing its inode
requires recreating the container so it sees the replacement.

Private administration stays on a mode-0600 Unix socket:

```sh
docker compose exec -T relay ghostyctl devices --admin-socket /data/admin.sock
docker compose exec -T relay ghostyctl revoke --admin-socket /data/admin.sock --device DEVICE_ID
```

Invitation issuance and legacy invitation credentials are disabled in production.
Local experiments can start `hesper-relay --dev-invitations` on its default loopback
listener. This flag rejects non-loopback bind addresses.

Back up the database while stopped or with a bbolt-consistent snapshot. This is a
single-instance relay: do not run replicas against the same volume. Caddy terminates
TLS; the relay is trusted with terminal traffic. Access logs containing OAuth
callback queries should not be enabled. Default relay logs omit terminal payloads
and credentials.
