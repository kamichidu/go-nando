# Install nando

## Debian and Ubuntu

Download the Linux amd64 `.deb` package from [GitHub Releases](https://github.com/kamichidu/go-nando/releases), then install it with dpkg:

```sh
sudo dpkg -i ./go-nando_*_linux_amd64.deb
```

The package installs:

- `nando` under `/opt/nando/bin` and the `/usr/bin/nando` symlink.
- Adapters under `/opt/nando/libexec`.

Installation does not initialize a repository or enable or start a daemon. Continue with the [Quick Start for FUSE or WebDAV use](./quickstart). For a packaged system repository, follow the [systemd system-service setup](../setup/systemd), which initializes the repository under the dedicated service account.

## Archives

Releases provide archives for supported Linux, macOS, and Windows architectures. Extract an archive and add its executable directory to `PATH`. FUSE and WebDAV adapters are available only in archives that include them for that platform.

## Build from source

Use the committed `go.work` from a development checkout and Go 1.25.1 or later:

```sh
go install ./cmd/nando
go install ./adapters/fuse/cmd/nando-fuse
go install ./adapters/webdav/cmd/nando-webdav
```

For general use, install a GitHub Release or Debian package. The nested Go modules rely on the checkout workspace.
