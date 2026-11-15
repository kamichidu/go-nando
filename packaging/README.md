# Debian package contents

User-facing installation and service procedures are maintained in the
[nando manual](https://kamichidu.github.io/go-nando/). This file records only
Debian package-specific behavior.

The `go-nando` package installs the `nando` executable in `/opt/nando/bin`,
adapters in `/opt/nando/libexec`, and `/usr/bin/nando` as a symlink. It also
installs separate system and user `nando@.service` templates under the systemd
unit directories and example TOML files under `/usr/share/doc/go-nando/`.

The system template runs as the dedicated non-login `nando` account. Its Unix
socket is owner-only, so all clients for that instance must run as the same
account. The package does not enable, start, or restart services and does not
initialize or migrate repositories. Remove and purge preserve repository data,
configuration, credentials, and the service account.

`packaging/check-deb.sh` checks package paths and ownership. The CI
`packaging/system_service_smoke.py` check installs the package in an isolated
systemd container, initializes a Local repository, starts the packaged system
unit, connects with `nando doctor`, and stops the service.
