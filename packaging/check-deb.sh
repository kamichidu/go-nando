#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_DIR=${BUILD_DIR:-"$ROOT/.build/deb-check"}
VERSION=${VERSION:-0.0.0-packaging-check}
export BUILD_DIR VERSION

"$ROOT/packaging/build-deb.sh"

PACKAGE=$(find "$BUILD_DIR" -maxdepth 1 -type f -name 'go-nando_*.deb' -print -quit)
if [ -z "$PACKAGE" ]; then
	echo "no Debian package produced in $BUILD_DIR" >&2
	exit 1
fi

CONTENTS=$(dpkg-deb --contents "$PACKAGE")
printf '%s\n' "$CONTENTS" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/bin/nando$'
printf '%s\n' "$CONTENTS" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/libexec/nando-fuse$'
printf '%s\n' "$CONTENTS" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/libexec/nando-webdav$'
printf '%s\n' "$CONTENTS" | grep -Fq './usr/bin/nando -> /opt/nando/bin/nando'
if printf '%s\n' "$CONTENTS" | grep -Eq '\./usr/bin/nando-(fuse|webdav)'; then
	echo "package unexpectedly exposes adapter symlinks in /usr/bin" >&2
	exit 1
fi

if printf '%s\n' "$CONTENTS" | grep -Eq '\./etc/nando/|\./var/lib/nando/'; then
	echo "package unexpectedly contains configuration or persistent state" >&2
	exit 1
fi

UNIT_CONTENTS=$(dpkg-deb --fsys-tarfile "$PACKAGE" | tar -xOf - ./usr/lib/systemd/user/nando@.service)
printf '%s\n' "$UNIT_CONTENTS" | grep -Fq 'ExecStart=/opt/nando/bin/nando '

echo "Debian package layout checks passed: $PACKAGE"
