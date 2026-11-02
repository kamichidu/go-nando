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
for binary in nando nando-fuse nando-webdav; do
	printf '%s\n' "$CONTENTS" | grep -Eq "^-rwxr-xr-x .*\./opt/nando/bin/$binary$"
	printf '%s\n' "$CONTENTS" | grep -Fq "./usr/bin/$binary -> /opt/nando/bin/$binary"
done

if printf '%s\n' "$CONTENTS" | grep -Eq '\./etc/nando/|\./var/lib/nando/'; then
	echo "package unexpectedly contains configuration or persistent state" >&2
	exit 1
fi

UNIT_CONTENTS=$(dpkg-deb --fsys-tarfile "$PACKAGE" | tar -xOf - ./usr/lib/systemd/user/nando@.service)
printf '%s\n' "$UNIT_CONTENTS" | grep -Fq 'ExecStart=/opt/nando/bin/nando '

echo "Debian package layout checks passed: $PACKAGE"
