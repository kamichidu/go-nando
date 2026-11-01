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
tr -d '\r' <"$TMP/headers" | grep -Eqi '^DAV:[[:space:]]*1$'
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
request 501 -X PUT -H 'If: (<opaquelocktoken:unsupported>)' --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/file"; cmp "$TMP/payload" "$TMP/body"
request 501 -X PUT -H 'If-Modified-Since: Thu, 02 Jan 2025 03:04:05 GMT' --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 204 -X PUT -H "If-Match: $ETAG" --data-binary @"$TMP/winner" "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/file"; cmp "$TMP/winner" "$TMP/body"
request 201 -X MKCOL "$E2E_WEBDAV_URL/collection"
printf '<propfind xmlns="DAV:"><allprop/></propfind>' >"$TMP/propfind"
request 207 -X PROPFIND -H 'Depth: 1' -H 'Content-Type: application/xml' --data-binary @"$TMP/propfind" "$E2E_WEBDAV_URL/"
python3 - "$TMP/body" <<'PY'
import sys
import xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
ns = {"d": "DAV:"}
assert not root.findall(".//d:supportedlock", ns), "allprop advertised unsupported locks"
assert any(item.find("d:resourcetype/d:collection", ns) is not None for item in root.findall(".//d:prop", ns))
PY
printf '<propfind xmlns="DAV:"><propname/></propfind>' >"$TMP/propfind-propname"
for depth in 0 1; do
  request 207 -X PROPFIND -H "Depth: $depth" -H 'Content-Type: application/xml' --data-binary @"$TMP/propfind-propname" "$E2E_WEBDAV_URL/"
  python3 - "$TMP/body" <<'PY'
import sys
import xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
assert not root.findall(".//{DAV:}supportedlock"), "propname advertised unsupported locks"
PY
done
printf '<propfind xmlns="DAV:"><prop><supportedlock/></prop></propfind>' >"$TMP/propfind-supportedlock"
for depth in 0 1; do
  request 207 -X PROPFIND -H "Depth: $depth" -H 'Content-Type: application/xml' --data-binary @"$TMP/propfind-supportedlock" "$E2E_WEBDAV_URL/"
  python3 - "$TMP/body" <<'PY'
import sys
import xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
ns = {"d": "DAV:"}
found = 0
for propstat in root.findall(".//d:propstat", ns):
    if propstat.find("d:prop/d:supportedlock", ns) is not None:
        found += 1
        assert propstat.findtext("d:status", namespaces=ns).endswith("404 Not Found")
assert found > 0, "explicit supportedlock was omitted instead of returned as 404"
assert not root.findall(".//d:lockentry", ns), "unsupported lockentry was advertised"
PY
done
request 201 -X MOVE -H "Destination: $E2E_WEBDAV_URL/moved" "$E2E_WEBDAV_URL/file"
request 404 "$E2E_WEBDAV_URL/file"
request 200 "$E2E_WEBDAV_URL/moved"; cmp "$TMP/winner" "$TMP/body"
request 201 -X COPY -H "Destination: $E2E_WEBDAV_URL/copied" "$E2E_WEBDAV_URL/moved"
request 200 "$E2E_WEBDAV_URL/copied"; cmp "$TMP/winner" "$TMP/body"
request 412 -X COPY -H "Destination: $E2E_WEBDAV_URL/copied" "$E2E_WEBDAV_URL/moved"
request 501 -X LOCK "$E2E_WEBDAV_URL/moved"
request 501 -X UNLOCK -H 'Lock-Token: <opaquelocktoken:unsupported>' "$E2E_WEBDAV_URL/moved"
request 501 -X PROPPATCH "$E2E_WEBDAV_URL/moved"
request 204 -X DELETE "$E2E_WEBDAV_URL/copied"
request 404 "$E2E_WEBDAV_URL/copied"
echo 'PASS WebDAV curl CRUD, metadata, move/copy, ETag preconditions, unsupported contract boundaries'

RESULTS=${E2E_ARTIFACTS_DIR:-$TMP}/webdav-results.tsv
record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$RESULTS"; }
: >"$RESULTS"
record curl-basic PASS 'GET/HEAD/PUT/DELETE/MKCOL and repository bytes verified'
record curl-http PASS 'OPTIONS, ETag and supported ETag conditions verified; unsupported DAV/HTTP PUT conditions explicitly rejected'
record curl-propfind PASS 'allprop/propname omit supportedlock; explicit supportedlock is 404 at Depth 0/1; Depth 1 retains collections'
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
