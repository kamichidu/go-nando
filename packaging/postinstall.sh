#!/bin/sh
set -eu

if ! getent group nando >/dev/null 2>&1; then
	groupadd --system nando
fi

if ! getent passwd nando >/dev/null 2>&1; then
	useradd --system --gid nando --home-dir /var/lib/nando --shell /usr/sbin/nologin --no-create-home nando
fi

systemctl daemon-reload
