#!/bin/bash
set -euo pipefail

main() {
	local root build_dir version package contents unit_contents expected_version
	root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
	build_dir=${BUILD_DIR:-"${root}/.build/deb-check"}
	version=${VERSION:-0.0.0-packaging-check}

	if [[ "$#" -gt 1 ]]; then
		printf 'usage: %s [existing-package.deb]\n' "$0" >&2
		return 2
	fi
	if [[ "$#" -eq 1 ]]; then
		package=$(realpath -- "${1}")
	else
		BUILD_DIR="${build_dir}" VERSION="${version}" "${root}/packaging/build-deb.sh"
		package=$(find "${build_dir}" -maxdepth 1 -type f -name 'go-nando_*.deb' -print -quit)
		if [[ -z "${package}" ]]; then
			printf 'no Debian package produced in %s\n' "${build_dir}" >&2
			return 1
		fi
	fi
	if [[ ! -f "${package}" ]]; then
		printf 'Debian package not found: %s\n' "${package}" >&2
		return 1
	fi

	contents=$(dpkg-deb --contents "${package}")
	printf '%s\n' "${contents}" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/bin/nando$'
	printf '%s\n' "${contents}" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/libexec/nando-fuse$'
	printf '%s\n' "${contents}" | grep -Eq '^-rwxr-xr-x .*\./opt/nando/libexec/nando-webdav$'
	printf '%s\n' "${contents}" | grep -Fq './usr/bin/nando -> /opt/nando/bin/nando'
	if printf '%s\n' "${contents}" | grep -Eq '\./usr/bin/nando-(fuse|webdav)'; then
		printf 'package unexpectedly exposes adapter symlinks in /usr/bin\n' >&2
		return 1
	fi

	if printf '%s\n' "${contents}" | grep -Eq '\./etc/nando/|\./var/lib/nando/'; then
		printf 'package unexpectedly contains configuration or persistent state\n' >&2
		return 1
	fi

	unit_contents=$(dpkg-deb --fsys-tarfile "${package}" | tar -xOf - ./usr/lib/systemd/user/nando@.service)
	printf '%s\n' "${unit_contents}" | grep -Fq 'ExecStart=/opt/nando/bin/nando '
	if [[ -n "${NFPM_ARCH:-}" ]]; then
		[[ "$(dpkg-deb --field "${package}" Architecture)" == "${NFPM_ARCH}" ]]
	fi
	if [[ -n "${VERSION:-}" ]]; then
		expected_version=${VERSION#v}
		expected_version=${expected_version/-/\~}
		[[ "$(dpkg-deb --field "${package}" Version)" == "${expected_version}" ]]
	fi

	printf 'Debian package layout checks passed: %s\n' "${package}"
}

main "$@"
