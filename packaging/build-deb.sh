#!/bin/bash
set -euo pipefail

main() {
	local root version build_dir nfpm_arch goos goarch cgo_enabled nfpm_config
	root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
	version=${VERSION:-$(git -C "${root}" describe --tags --always --dirty)}
	build_dir=${BUILD_DIR:-"${root}/.build/deb"}
	nfpm_arch=${NFPM_ARCH:-$(dpkg --print-architecture)}
	goos=${GOOS:-linux}
	goarch=${GOARCH:-$(go env GOARCH)}
	cgo_enabled=${CGO_ENABLED:-0}
	export VERSION="${version}" BUILD_DIR="${build_dir}" NFPM_ARCH="${nfpm_arch}"

	mkdir -p "${build_dir}"
	nfpm_config="${build_dir}/nfpm.yaml"
	sed \
		-e "s|\${VERSION}|${version}|g" \
		-e "s|\${BUILD_DIR}|${build_dir}|g" \
		-e "s|\${NFPM_ARCH}|${nfpm_arch}|g" \
		"${root}/packaging/nfpm.yaml" > "${nfpm_config}"
	(
		cd "${root}"
		CGO_ENABLED="${cgo_enabled}" GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags='-s -w' -o "${build_dir}/nando" ./cmd/nando
		(
			cd adapters/fuse
			CGO_ENABLED="${cgo_enabled}" GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags='-s -w' -o "${build_dir}/nando-fuse" ./cmd/nando-fuse
		)
		(
			cd adapters/webdav
			CGO_ENABLED="${cgo_enabled}" GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags='-s -w' -o "${build_dir}/nando-webdav" ./cmd/nando-webdav
		)
		nfpm package --config "${nfpm_config}" --packager deb --target "${build_dir}"
	)
}

main "$@"
