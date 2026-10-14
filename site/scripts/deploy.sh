#!/bin/sh
# Deploy the static site next to the relay's shared nginx + certbot stack.
#   scripts/deploy.sh site   build, upload, (re)start the site container
#   scripts/deploy.sh acme   install the HTTP-only vhost for the first certificate
#   scripts/deploy.sh tls    obtain the certificate, install the HTTPS vhost
# Settings come from the environment (see the defaults below) and
# the relay's deploy/local.env (SITE_ENV_FILE) for the server and proxy names.
set -eu
cd "$(dirname "$0")/.."
env_file=${SITE_ENV_FILE:-../relay/deploy/local.env}
# Read KEY=value lines literally (values may contain spaces); the environment wins.
if [ -f "$env_file" ]; then
  while IFS='=' read -r k v; do
    case "$k" in RELAY_*) eval "[ -n \"\${$k:-}\" ] || $k=\$v" ;; esac
  done < "$env_file"
fi
SITE_DOMAIN=${SITE_DOMAIN:-hesper.wtf}
SERVER=${SITE_SERVER:-${RELAY_SERVER:?set RELAY_SERVER}}
REMOTE_DIR=${SITE_REMOTE_DIR:-/opt/hesper-site}
PROXY_DIR=${RELAY_PROXY_DIR:?set RELAY_PROXY_DIR}
PROXY_NETWORK=${RELAY_PROXY_NETWORK:?}
PROXY_NGINX=${RELAY_PROXY_NGINX:?}
PROXY_ARGS=${RELAY_PROXY_COMPOSE_ARGS:-}
CONF="$PROXY_DIR/nginx/conf.d/hesper-site.conf"
SSH="ssh -o IdentitiesOnly=no -o StrictHostKeyChecking=yes -o ControlMaster=auto -o ControlPath=/tmp/hesper-site-ssh-%r@%h:%p -o ControlPersist=600"
DEST="root@$SERVER"
render() { sed "s/\${SITE_DOMAIN}/$SITE_DOMAIN/g" "deploy/$1"; }
reload() { $SSH "$DEST" "docker exec $PROXY_NGINX nginx -t && docker exec $PROXY_NGINX nginx -s reload"; }
case "${1:-}" in
  site)
    SITE_URL="https://$SITE_DOMAIN" npm run build
    $SSH "$DEST" "mkdir -p $REMOTE_DIR"
    rsync -az --delete -e "$SSH" out/ "$DEST:$REMOTE_DIR/out/"
    rsync -az -e "$SSH" deploy/compose.yaml deploy/static.conf "$DEST:$REMOTE_DIR/"
    $SSH "$DEST" "cd $REMOTE_DIR && PROXY_NETWORK=$PROXY_NETWORK docker compose -p hesper-site up -d"
    ;;
  acme)
    render nginx.acme.conf.template | $SSH "$DEST" "cat > $CONF"
    reload ;;
  tls)
    $SSH "$DEST" "cd $PROXY_DIR && docker compose $PROXY_ARGS run --rm --no-deps --entrypoint certbot certbot certonly --non-interactive --webroot -w /var/www/certbot --cert-name $SITE_DOMAIN -d $SITE_DOMAIN -d www.$SITE_DOMAIN --keep-until-expiring"
    render nginx.conf.template | $SSH "$DEST" "cat > $CONF"
    reload ;;
  *) echo "usage: scripts/deploy.sh site|acme|tls" >&2; exit 2 ;;
esac
