#!/usr/bin/env bash
set -Eeuo pipefail

ROOT=${1:?repository root required}
DESIGN_IR_REPO=${2:?Design IR checkout required}
cd "$ROOT"
{
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
