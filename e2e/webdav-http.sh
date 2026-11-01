#!/usr/bin/env bash
set -Eeuo pipefail
: "${E2E_WEBDAV_URL:?}"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/go-nando-webdav.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
printf 'Phase A WebDAV over curl\n' >"$TMP/payload"

request() {
  local expected=$1; shift
  local status
  status=$(curl --silent --show-error --max-time 15 -D "$TMP/headers" -o "$TMP/body" -w '%{http_code}' "$@")
  if [[ "$status" != "$expected" ]]; then
    printf 'expected HTTP %s, got %s for:' "$expected" "$status" >&2
    printf ' %q' "$@" >&2
    printf '\nresponse: ' >&2; cat "$TMP/body" >&2; printf '\n' >&2
    exit 1
  fi
}

request 200 -X OPTIONS "$E2E_WEBDAV_URL/"
grep -qi '^DAV: 1' "$TMP/headers"
request 201 -X PUT --data-binary @"$TMP/payload" "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/file"
cmp "$TMP/payload" "$TMP/body"
request 200 -I "$E2E_WEBDAV_URL/file"
ETAG=$(awk 'tolower($1)=="etag:" {gsub("\r", "", $2); print $2}' "$TMP/headers")
if [[ -z "$ETAG" ]]; then echo 'GET/HEAD did not return ETag' >&2; exit 1; fi
printf 'winner\n' >"$TMP/winner"
request 412 -X PUT -H 'If-Match: "stale"' --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/file"; cmp "$TMP/payload" "$TMP/body"
request 412 -X PUT -H 'If-None-Match: *' --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 204 -X PUT -H "If-Match: $ETAG" --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/file"; cmp "$TMP/winner" "$TMP/body"
request 201 -X MKCOL "$E2E_WEBDAV_URL/collection"
printf '<propfind xmlns="DAV:"><allprop/></propfind>' >"$TMP/propfind"
request 207 -X PROPFIND -H 'Depth: 1' -H 'Content-Type: application/xml' --data-binary @"$TMP/propfind" "$E2E_WEBDAV_URL/"
grep -q 'collection' "$TMP/body"
request 201 -X MOVE -H "Destination: $E2E_WEBDAV_URL/moved" "$E2E_WEBDAV_URL/file"
request 404 "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/moved"; cmp "$TMP/winner" "$TMP/body"
request 201 -X COPY -H "Destination: $E2E_WEBDAV_URL/copied" "$E2E_WEBDAV_URL/moved"
request 200 "$E2E_WEBDAV_URL/copied"; cmp "$TMP/winner" "$TMP/body"
request 412 -X COPY -H "Destination: $E2E_WEBDAV_URL/copied" "$E2E_WEBDAV_URL/moved"
request 501 -X LOCK "$E2E_WEBDAV_URL/moved"
request 501 -X PROPPATCH "$E2E_WEBDAV_URL/moved"
request 204 -X DELETE "$E2E_WEBDAV_URL/copied"
request 404 "$E2E_WEBDAV_URL/copied"
echo 'PASS WebDAV curl CRUD, metadata, move/copy, ETag preconditions, unsupported contract boundaries'

RESULTS=${E2E_ARTIFACTS_DIR:-$TMP}/webdav-results.tsv
record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$RESULTS"; }
: >"$RESULTS"
record curl-basic PASS 'GET/HEAD/PUT/DELETE/MKCOL and repository bytes verified'
record curl-http PASS 'OPTIONS, ETag, If-Match and If-None-Match verified over HTTP'
record curl-propfind PASS 'PROPFIND depth 1 returned a 207 multistatus including the created collection'
record curl-file-copymove PASS 'file COPY to absent destination and MOVE verified; overwrite remains unsupported'
record curl-locks SKIP 'LOCK/UNLOCK are deliberately unsupported; a separate assertion confirmed 501'
record curl-dead-properties SKIP 'PROPPATCH/dead properties are deliberately unsupported; a separate assertion confirmed 501'
for group in basic copymove props locks http; do
  if command -v litmus >/dev/null 2>&1; then
    record "litmus-$group" SKIP 'Litmus available but full compliance run is outside the configured supported method surface'
  else
    record "litmus-$group" SKIP 'Litmus executable is not installed on this host'
  fi
done
if command -v cadaver >/dev/null 2>&1; then
  record cadaver SKIP 'cadaver installed but interactive client session not run by automated gate'
else
  record cadaver SKIP 'cadaver executable is not installed on this host'
fi
