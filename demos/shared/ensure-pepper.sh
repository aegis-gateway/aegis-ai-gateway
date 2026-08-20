#!/usr/bin/env bash
# Ensures the given .env file carries an AEGIS_KEY_PEPPER of at least 32 chars.
#
# The gateway and keygen both refuse to start without one, and Compose only
# passes provider config to the gateway through env_file — so a demo that does
# not write a pepper into .env starts a container that exits immediately and
# leaves wait-for-gateway.sh to time out.
#
# A demo pepper is generated fresh rather than committed to the repo: it is not
# a secret worth publishing, and a value living in version control invites
# someone to carry it into production. Regenerating is safe here because the
# seeded demo key is a v1 SHA-256 hash, which does not involve the pepper.
set -euo pipefail

ENV_FILE="${1:-.env}"

if [ -f "$ENV_FILE" ] && grep -qE '^AEGIS_KEY_PEPPER=.{32,}$' "$ENV_FILE"; then
  exit 0
fi

# Drop any short or empty placeholder line before appending a real one.
if [ -f "$ENV_FILE" ] && grep -qE '^AEGIS_KEY_PEPPER=' "$ENV_FILE"; then
  tmp=$(mktemp)
  grep -vE '^AEGIS_KEY_PEPPER=' "$ENV_FILE" > "$tmp"
  mv "$tmp" "$ENV_FILE"
fi

if command -v openssl >/dev/null 2>&1; then
  pepper=$(openssl rand -hex 32)
else
  pepper=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
fi

printf '\n# Auto-generated for this demo run; not a production secret.\nAEGIS_KEY_PEPPER=%s\n' "$pepper" >> "$ENV_FILE"
echo "Generated AEGIS_KEY_PEPPER in ${ENV_FILE}" >&2
