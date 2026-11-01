#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty)}
BUILD_DIR=${BUILD_DIR:-"$ROOT/.build/deb"}
NFPM_ARCH=${NFPM_ARCH:-$(dpkg --print-architecture)}
export VERSION BUILD_DIR NFPM_ARCH

mkdir -p "$BUILD_DIR"
NFPM_CONFIG="$BUILD_DIR/nfpm.yaml"
sed \
	-e "s|\${VERSION}|$VERSION|g" \
	-e "s|\${BUILD_DIR}|$BUILD_DIR|g" \
	-e "s|\${NFPM_ARCH}|$NFPM_ARCH|g" \
	"$ROOT/packaging/nfpm.yaml" > "$NFPM_CONFIG"
(
	cd "$ROOT"
	go build -o "$BUILD_DIR/nando" ./cmd/nando
	(cd fuse && go build -o "$BUILD_DIR/nando-fuse" ./cmd/nando-fuse)
	(cd webdav && go build -o "$BUILD_DIR/nando-webdav" ./cmd/nando-webdav)
	nfpm package --config "$NFPM_CONFIG" --packager deb --target "$BUILD_DIR"
)
