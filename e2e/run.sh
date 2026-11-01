#!/usr/bin/env bash
set -Eeuo pipefail

ROOT=$(cd -- "$(dirname -- "$0")/.." && pwd)
STRESS=0
if [[ "${1:-}" == --stress ]]; then STRESS=1; shift; fi
if (($#)); then echo "usage: e2e/run.sh [--stress]" >&2; exit 2; fi

ARTIFACTS=$(mktemp -d "${TMPDIR:-/tmp}/go-nando-e2e.XXXXXX")
PROJECT="go-nando-e2e-$(date -u +%Y%m%d%H%M%S)-$$"
DESIGN_IR_REPO=${DESIGN_IR_REPO:-"$ROOT/../design-ir"}
DESIGN_IR_COMMIT=455ed318b7bc409d3e1da320620ef92d457042b9
COMPOSE=(docker compose --project-directory "$ROOT" -f "$ROOT/e2e/compose.yaml" -p "$PROJECT")
GATES="$ARTIFACTS/gates.tsv"
mkdir -p "$ARTIFACTS/bin" "$ARTIFACTS/state/local" "$ARTIFACTS/state/gcs" "$ARTIFACTS/run"
touch "$GATES"
export E2E_ARTIFACTS_DIR="$ARTIFACTS"
export E2E_NANDO_BIN="$ARTIFACTS/bin/nando"
export E2E_FUSE_BIN="$ARTIFACTS/bin/nando-fuse"
export E2E_SOCKET="$ARTIFACTS/run/nando.sock"
export E2E_REPOSITORY="$ARTIFACTS/state/local/repository"
export E2E_PROJECT="$PROJECT"
export E2E_FAKE_GCS=http://127.0.0.1:4443
overall=0
compose_started=0

mark() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$GATES"; }
run_gate() {
  local gate=$1 label=$2 timeout_s=$3; shift 3
  local output="$ARTIFACTS/${gate}.log"
  printf 'COMMAND %s: ' "$gate" | tee -a "$output"
  printf '%q ' "$@" | tee -a "$output"; printf '\n' | tee -a "$output"
  local rc=0
  timeout --signal=TERM --kill-after=10s "${timeout_s}s" "$@" >>"$output" 2>&1 || rc=$?
  if ((rc == 0)); then mark "$gate" PASS "$label"; else
    mark "$gate" FAIL "$label (exit $rc)"; tail -80 "$output" >&2; overall=1; return "$rc"
  fi
}
cleanup() {
  local rc=$?
  set +e
  if ((compose_started)); then
    timeout -k 5s 25s "${COMPOSE[@]}" ps --all >"$ARTIFACTS/compose-ps.log" 2>&1
    timeout -k 5s 25s "${COMPOSE[@]}" logs --no-color >"$ARTIFACTS/compose.log" 2>&1
    timeout -k 5s 35s "${COMPOSE[@]}" down --volumes --remove-orphans --timeout 8 >"$ARTIFACTS/compose-down.log" 2>&1
    if (($? != 0)); then mark CLEANUP FAIL "compose down failed; see compose-down.log"; overall=1; fi
  fi
  for gate in G0 G1 G2 G3; do
    if ! cut -f1 "$GATES" | rg -qx "$gate"; then mark "$gate" SKIP "stopped before this gate ran"; fi
  done
  if ((STRESS)) && ! cut -f1 "$GATES" | rg -qx G4; then mark G4 SKIP "stress requested but prerequisites did not complete"; fi
  echo "E2E evidence: $ARTIFACTS"
  exit $((rc != 0 ? rc : overall))
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v docker >/dev/null || ! docker compose version >/dev/null; then echo "FAIL: Docker Compose v2 required" >&2; exit 2; fi
if ! command -v fusermount3 >/dev/null || [[ ! -c /dev/fuse || ! -r /dev/fuse || ! -w /dev/fuse ]]; then
  mark G0 SKIP "host Linux FUSE prerequisites unavailable"
  mark G1 SKIP "host /dev/fuse or fusermount3 unavailable"
  mark G2 SKIP "native real FUSE runner unavailable"
  mark G3 SKIP "native real FUSE runner unavailable"
  exit 2
fi
if [[ ! -d "$DESIGN_IR_REPO/.git" || "$(git -C "$DESIGN_IR_REPO" rev-parse HEAD 2>/dev/null || true)" != "$DESIGN_IR_COMMIT" ]]; then
  echo "FAIL: DESIGN_IR_REPO must be at $DESIGN_IR_COMMIT" >&2; exit 2
fi
if ! command -v golangci-lint >/dev/null; then echo "FAIL: golangci-lint required" >&2; exit 2; fi

compose_started=1
if ! run_gate G0 "native root/FUSE tests, race, lint and pinned Design IR validation" 1800 bash -c '
  set -Eeuo pipefail
  compose=(docker compose --project-directory "$1" -f "$1/e2e/compose.yaml" -p "$2")
  "${compose[@]}" up -d fake-gcs
  exec bash "$1/e2e/static.sh" "$1" "$3"
' _ "$ROOT" "$PROJECT" "$DESIGN_IR_REPO"; then exit 1; fi

if ! run_gate G1 "host-native real FUSE CRUD/fsync/rename/unlink/writers/CAS" 480 bash -c '
  set -Eeuo pipefail
  "$1" --repository "$2" init --delivery-mode local
  export E2E_REPOSITORY="$2" E2E_SOCKET="$3"
  "$1" --repository "$2" daemon --listen "unix://$3" >"$4/daemon-local.log" 2>&1 & daemon=$!
  trap "kill -TERM \$daemon 2>/dev/null || true; wait \$daemon 2>/dev/null || true" EXIT
  python3 "$5/e2e/runner.py" wait-daemon
  python3 "$5/e2e/runner.py" health
  python3 "$5/e2e/runner.py" g1
  kill -TERM "$daemon"; wait "$daemon" || true
' _ "$E2E_NANDO_BIN" "$E2E_REPOSITORY" "$E2E_SOCKET" "$ARTIFACTS" "$ROOT"; then exit 1; fi

if ! run_gate G2 "host-native real FUSE with fake GCS outage/retry/idempotency" 600 bash -c '
  set -Eeuo pipefail
  compose=(docker compose --project-directory "$1" -f "$1/e2e/compose.yaml" -p "$2")
  export E2E_REPOSITORY="$3" E2E_SOCKET="$4" E2E_FAKE_GCS=http://127.0.0.1:4443
  export STORAGE_EMULATOR_HOST=http://127.0.0.1:4443 GOOGLE_CLOUD_PROJECT=nando-e2e
  "$5" --repository "$3" init --delivery-mode gcs --gcs-bucket nando-e2e
  python3 "$6/e2e/runner.py" g2-init
  "$5" --repository "$3" daemon --listen "unix://$4" >"$7/daemon-gcs.log" 2>&1 & daemon=$!
  trap "kill -TERM \$daemon 2>/dev/null || true; wait \$daemon 2>/dev/null || true" EXIT
  python3 "$6/e2e/runner.py" wait-daemon
  python3 "$6/e2e/runner.py" g2-normal
  "${compose[@]}" stop --timeout 3 fake-gcs
  python3 "$6/e2e/runner.py" g2-outage
  "${compose[@]}" start fake-gcs
  python3 "$6/e2e/runner.py" g2-retry
  kill -TERM "$daemon"; wait "$daemon" || true
' _ "$ROOT" "$PROJECT" "$ARTIFACTS/state/gcs/repository" "$ARTIFACTS/run/gcs.sock" "$E2E_NANDO_BIN" "$ROOT" "$ARTIFACTS"; then exit 1; fi

export E2E_REPOSITORY="$ARTIFACTS/state/local/repository" E2E_SOCKET="$ARTIFACTS/run/nando.sock"
if ! run_gate G3 "native daemon/FUSE crash, restart and recovery" 480 bash -c '
  set -Eeuo pipefail
  rm -rf "$2"
  "$1" --repository "$2" init --delivery-mode local
  "$1" --repository "$2" daemon --listen "unix://$3" >"$4/daemon-g3.log" 2>&1 & daemon=$!
  trap "kill -TERM \$daemon 2>/dev/null || true; wait \$daemon 2>/dev/null || true" EXIT
  export E2E_REPOSITORY="$2" E2E_SOCKET="$3"
  python3 "$5/e2e/runner.py" wait-daemon
  python3 "$5/e2e/runner.py" g3-commit
  kill -KILL "$daemon"; wait "$daemon" || true
  rm -f "$3"
  "$1" --repository "$2" daemon --listen "unix://$3" >>"$4/daemon-g3.log" 2>&1 & daemon=$!
  python3 "$5/e2e/runner.py" wait-daemon
  python3 "$5/e2e/runner.py" g3-verify
  python3 "$5/e2e/runner.py" g3-start-uncommitted
  python3 "$5/e2e/runner.py" g3-crash-fuse
  kill -KILL "$daemon"; wait "$daemon" || true
  rm -f "$3"
  "$1" --repository "$2" daemon --listen "unix://$3" >>"$4/daemon-g3.log" 2>&1 & daemon=$!
  python3 "$5/e2e/runner.py" wait-daemon
  python3 "$5/e2e/runner.py" g3-verify-recovery
' _ "$E2E_NANDO_BIN" "$E2E_REPOSITORY" "$E2E_SOCKET" "$ARTIFACTS" "$ROOT"; then exit 1; fi

if ((STRESS)); then
  if ! run_gate G4 "optional bounded native FUSE concurrent writer stress" 180 bash -c '
    set -Eeuo pipefail
    "$1" --repository "$2" daemon --listen "unix://$3" >"$4/daemon-g4.log" 2>&1 & daemon=$!
    trap "kill -TERM \$daemon 2>/dev/null || true; wait \$daemon 2>/dev/null || true" EXIT
    export E2E_REPOSITORY="$2" E2E_SOCKET="$3"
    python3 "$5/e2e/runner.py" wait-daemon
    python3 "$5/e2e/runner.py" g4
  ' _ "$E2E_NANDO_BIN" "$E2E_REPOSITORY" "$E2E_SOCKET" "$ARTIFACTS" "$ROOT"; then exit 1; fi
fi
