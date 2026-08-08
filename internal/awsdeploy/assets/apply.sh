#!/usr/bin/env bash
# Converge this instance onto the configuration it is handed. Runs on first boot from
# user-data, and again on every deploy over SSM.
#
# Why it exists: CloudFormation updates an EC2 instance's user-data IN PLACE (stop,
# modify, start) rather than replacing the instance, and cloud-init runs user-data
# once per instance. So a `--domain` added to an existing stack changed the security
# group but never reached the box, leaving Caddy serving plain HTTP with the template
# insisting otherwise. Everything that depends on deploy-time configuration therefore
# lives here, and deploy.sh runs it after every stack update.
#
# Inputs (environment):
#   TUNNEL_HOST     public address connectors dial; MN_RELAY_TUNNEL is derived from it
#   SITE_ADDRESS    hostname for automatic HTTPS, or ":80" for plain HTTP
#   ACME_EMAIL      optional Let's Encrypt contact address
#   WITH_DOWNLOADS  "1" to also build the CLI binaries served at /download
#   ENV_PARAMETER   optional SSM SecureString name holding the relay env file
set -euo pipefail

: "${TUNNEL_HOST:?TUNNEL_HOST is required}"
SITE_ADDRESS="${SITE_ADDRESS:-:80}"
ACME_EMAIL="${ACME_EMAIL:-}"
WITH_DOWNLOADS="${WITH_DOWNLOADS:-0}"
ENV_PARAMETER="${ENV_PARAMETER:-}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

# First boot and a deploy can land at the same moment; two concurrent `compose up
# --build` runs on the same project fight over the image and the containers.
exec 9>/var/lock/moony-apply
flock 9

install -d -m 0700 /etc/moony

# The relay's own environment. Rewritten from scratch each time so a setting removed
# from the SSM parameter actually disappears from the box.
if [ -n "$ENV_PARAMETER" ]; then
	# Straight to a 0600 file: the value never reaches the console, a log, or the
	# process table.
	install -m 0600 /dev/null /etc/moony/relay.env
	aws ssm get-parameter --name "$ENV_PARAMETER" --with-decryption \
		--query Parameter.Value --output text > /etc/moony/relay.env
else
	install -m 0600 /dev/null /etc/moony/relay.env
	echo "# no ENV_PARAMETER given: the relay runs WITHOUT authentication" > /etc/moony/relay.env
fi

# Authoritative whatever the parameter said: connectors must dial the public address,
# and getting this wrong is the classic failure mode.
sed -i '/^MN_RELAY_TUNNEL=/d' /etc/moony/relay.env
echo "MN_RELAY_TUNNEL=${TUNNEL_HOST}:8771" >> /etc/moony/relay.env

cat > /etc/moony/compose.env <<EOF
SITE_ADDRESS=${SITE_ADDRESS}
WITH_DOWNLOADS=${WITH_DOWNLOADS}
EOF

# Caddy's global options import this. An empty value cannot be passed as a directive
# argument, so the choice is made here rather than in the Caddyfile.
if [ -n "$ACME_EMAIL" ]; then
	echo "email ${ACME_EMAIL}" > /etc/moony/caddy-acme.conf
else
	echo "# no ACME contact address configured" > /etc/moony/caddy-acme.conf
fi
chmod 644 /etc/moony/caddy-acme.conf

cd "$root"
docker compose --env-file /etc/moony/compose.env \
	-f internal/awsdeploy/assets/compose.aws.yml up -d --build

docker compose --env-file /etc/moony/compose.env \
	-f internal/awsdeploy/assets/compose.aws.yml ps
echo "moony-apply: converged on SITE_ADDRESS=${SITE_ADDRESS}"
