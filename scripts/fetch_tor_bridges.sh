#!/usr/bin/env bash
# Fetches Tor bridges for the setup wizard's "Retry with Tor" into assets/tor/bridges.json:
# Tor Project's recommended bridges for Iran (Moat circumvention settings, the API Tor Browser's
# Connection Assist uses; no captcha), plus Tor's built-in meek-azure — Moat doesn't hand it
# out, but it's the transport most likely to work where obfs4 ports are scanned and closed.
# Required before building, as pubspec.yaml lists assets/tor/. The bridges are only ever in
# builds, never in the repository.
set -euo pipefail
cd "$(dirname "$0")"

COUNTRY=ir
OUT=../assets/tor/bridges.json

response=$(curl -fsSL --retry 3 -X POST \
  -H 'Content-Type: application/vnd.api+json' \
  -d "{\"country\":\"$COUNTRY\",\"transports\":[\"webtunnel\",\"obfs4\",\"snowflake\"]}" \
  https://bridges.torproject.org/moat/circumvention/settings)

# Bridge lines hold no quotes: each is a JSON string "<transport> <address> <fingerprint> …".
mapfile -t bridges < <(grep -oE '"(webtunnel|obfs4|snowflake) [^"]+ [0-9A-F]{40}[^"]*"' <<<"$response" | tr -d '"' | sort -u)

# Tor's built-in bridges (what Tor Browser's "Connect" uses): take its meek-azure line. Same
# endpoint family as the settings call above.
builtin=$(curl -fsSL --retry 3 https://bridges.torproject.org/moat/circumvention/builtin)
if meek=$(grep -oE '"meek_lite [^"]+"' <<<"$builtin" | head -1 | tr -d '"') && [ -n "$meek" ]; then
  bridges+=("$meek")
fi

# Nothing here prints a bridge: build logs may be public.
if [ "${#bridges[@]}" -eq 0 ]; then
  echo "No bridges in Tor Project's answer" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"
{
  echo '{'
  echo "  \"country\": \"$COUNTRY\","
  echo "  \"fetchedAt\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
  echo '  "bridges": ['
  for i in "${!bridges[@]}"; do
    sep=','; [ "$i" -eq $((${#bridges[@]} - 1)) ] && sep=''
    echo "    \"${bridges[$i]}\"$sep"
  done
  echo '  ]'
  echo '}'
} > "$OUT"
echo "Wrote ${#bridges[@]} bridges to $OUT:"
cut -d' ' -f1 <<<"$(printf '%s\n' "${bridges[@]}")" | sort | uniq -c
