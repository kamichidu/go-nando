#!/usr/bin/env bash
set -Eeuo pipefail

ROOT=${1:?repository root required}
DESIGN_IR_REPO=${2:?Design IR checkout required}
cd "$ROOT"
{
  echo '$ test "$(go env GOWORK)" = "$ROOT/go.work"'
  test "$(go env GOWORK)" = "$ROOT/go.work"
  echo '$ go test ./...'
  go test ./...
  echo '$ go test -race ./...'
  go test -race ./...
  echo '$ golangci-lint config verify'
  golangci-lint config verify
  echo '$ golangci-lint run ./...'
  golangci-lint run ./...
  echo '$ go build -o E2E_NANDO_BIN ./cmd/nando'
  go build -o "$E2E_NANDO_BIN" ./cmd/nando
  echo '$ GOBIN=workspace-install go install ./...'
  install_bin="$E2E_ARTIFACTS_DIR/workspace-install"
  mkdir -p "$install_bin"
  GOBIN="$install_bin" go install ./...
  test -x "$install_bin/nando"
  test ! -e "$install_bin/nando-fuse"
  test ! -e "$install_bin/nando-webdav"
  echo '$ GOBIN=workspace-install go install ./cmd/nando'
  GOBIN="$install_bin" go install ./cmd/nando
  echo '$ GOBIN=workspace-install go install ./adapters/fuse/cmd/nando-fuse'
  GOBIN="$install_bin" go install ./adapters/fuse/cmd/nando-fuse
  echo '$ GOBIN=workspace-install go install ./adapters/webdav/cmd/nando-webdav'
  GOBIN="$install_bin" go install ./adapters/webdav/cmd/nando-webdav
  test -x "$install_bin/nando"
  test -x "$install_bin/nando-fuse"
  test -x "$install_bin/nando-webdav"
  echo '$ go test ./... (fuse module)'
  (cd adapters/fuse && go test ./...)
  echo '$ go test -race ./... (fuse module)'
  (cd adapters/fuse && go test -race ./...)
  echo '$ go test ./... (WebDAV module)'
  (cd adapters/webdav && go test ./...)
  echo '$ go test -race ./... (WebDAV module)'
  (cd adapters/webdav && go test -race ./...)
  echo '$ golangci-lint config verify (fuse module)'
  (cd adapters/fuse && golangci-lint config verify)
  echo '$ golangci-lint run ./... (fuse module)'
  (cd adapters/fuse && golangci-lint run ./...)
  echo '$ go build -o E2E_FUSE_BIN ./cmd/nando-fuse (fuse module)'
  (cd adapters/fuse && go build -o "$E2E_FUSE_BIN" ./cmd/nando-fuse)
  echo '$ golangci-lint config verify (WebDAV module)'
  (cd adapters/webdav && golangci-lint config verify)
  echo '$ golangci-lint run ./... (WebDAV module)'
  (cd adapters/webdav && golangci-lint run ./...)
  echo '$ go build -o E2E_WEBDAV_BIN ./cmd/nando-webdav (WebDAV module)'
  (cd adapters/webdav && go build -o "$E2E_WEBDAV_BIN" ./cmd/nando-webdav)
  echo '$ tools/validate-design-ir.sh (pinned checkout)'
  DESIGN_IR_REPO="$DESIGN_IR_REPO" tools/validate-design-ir.sh
} 2>&1 | tee "$E2E_ARTIFACTS_DIR/G0-static.log"
