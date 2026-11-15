# Install nando

## Debian and Ubuntu

Download the Linux amd64 `.deb` package from [GitHub Releases](https://github.com/kamichidu/go-nando/releases), then install it with dpkg:

```sh
sudo dpkg -i ./go-nando_*_linux_amd64.deb
```

The package installs `nando` under `/opt/nando/bin`, adapters under `/opt/nando/libexec`, and the `/usr/bin/nando` symlink. It does not initialize a repository or enable or start a daemon. Continue with the [local quickstart](./quickstart) or [systemd system service setup](../setup/systemd).

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
