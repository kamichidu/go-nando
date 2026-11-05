# Debian package and systemd instances

The `go-nando` deb installs `nando` in `/opt/nando/bin` and the adapter
executables in `/opt/nando/libexec`. Only `/usr/bin/nando` is exposed; use
`nando fuse` and `nando webdav` to launch adapters. It also contains two
independent `nando@.service` templates: one for the system manager and one for
each user's manager. The user service is the supported Phase A deployment.
The system service is experimental, is not a Phase A completion gate, and has
additional same-account restrictions described below.

Build with nfpm installed:

```sh
VERSION=1.2.3 packaging/build-deb.sh
```

GitHub Releases publish Linux amd64 and arm64 archives and an amd64 Debian
package, plus macOS and Windows amd64 and arm64 archives. CI cross-compiles the
macOS and Windows executables on Linux; it does not run them on those operating
systems. Their runtime behavior is therefore not covered by release CI.

Run `packaging/check-deb.sh` to build a temporary package and check its binary,
symlink, and service-unit paths. The package owns both adapter files in
`/opt/nando/libexec`, `nando` in `/opt/nando/bin`, and the `/usr/bin/nando`
symlink through dpkg metadata; maintainer scripts do not create unmanaged
command links.

The package creates and retains a non-login `nando` system account for the
experimental system template. Install/upgrade do not enable, start, or restart
services. Maintainer scripts reload system unit definitions only; users reload
their user manager when needed. Remove/purge preserves the account, repository
data, recovery state, configuration, and credentials. Package scripts never
initialize or migrate a repository. User lingering is never enabled by the
package.

## Supported user service

Create one TOML file per instance at
`$XDG_CONFIG_HOME/nando/<instance>.toml` (normally
`~/.config/nando/<instance>.toml`). Set `repository` to an initialized
repository owned by that user. The process uses the user's XDG data/config,
ADC identity, and default UDS under `$XDG_RUNTIME_DIR/nando/`; the application
creates the private socket parent (0700) and socket (0600). For example:

```sh
mkdir -p ~/.config/nando
cp /usr/share/doc/go-nando/config.user.example.toml ~/.config/nando/work.toml
# Edit repository to an initialized repository owned by this user.
systemctl --user daemon-reload
systemctl --user enable --now nando@work.service
```

Do not copy the example's placeholder path literally. For remote object delivery, set
all four scheduler values in the instance TOML. The package does not select
operational timing or parallelism defaults. ADC remains host/user managed; use
the user's native identity or a private credential file and ensure the user
manager receives any required `GOOGLE_APPLICATION_CREDENTIALS` environment.
Optional lingering for operation without a login session is an administrator
or user choice and is not changed by installation.

The unit permits repository writes under `~/.local/share/nando` and runtime
writes under `$XDG_RUNTIME_DIR/nando`. For a repository elsewhere or a custom
`XDG_DATA_HOME`, add its existing path to a per-instance systemd drop-in's
`ReadWritePaths=` before starting the service. Initialize the repository before
starting because the unit's write allowlist expects the repository path to
exist.

Different instance names use separate config files. The default socket derives
from the configured repository path, so distinct repositories get distinct
endpoints. Do not run multiple instances against the same repository.

## Experimental system service (not Phase A supported operation)

The system template uses `/etc/nando/<instance>.toml`,
`/var/lib/nando/<instance>` and `/run/nando-<instance>`. Copy
`config.system.example.toml`, replace `<instance>` with the exact unit instance
name, set its repository and explicit UDS endpoint, and initialize the
repository as the `nando` account before starting the unit. The system account
is non-login and is created by the package; it remains after remove/purge.
State and credentials are never deleted by package scripts. Configure ADC via
the host's machine identity or a host-provisioned credential readable only by
the service account; never put secrets in the unit/package.

The daemon's socket remains owner-only (0600). Therefore all system-scope
clients, including FUSE and WebDAV, must run as the same `nando` account and use
the matching endpoint. A normal login user's clients cannot connect. Do not
weaken the socket permissions to make cross-user access work. This template is
provided for experimentation only and is not required to pass Phase A.

FUSE and WebDAV remain separate client processes. Start the daemon before
either client, and stop clients before the daemon. WebDAV remains loopback-only
and unauthenticated under the approved Phase A boundary. The daemon does not
require network availability at startup; remote delivery retries asynchronously.

## Upgrade and removal

Review installed units and release notes after upgrades, then restart only the
instances that should load the new executable. No restart is performed during
upgrade because daemon shutdown can leave recoverable sessions. Removing the
package removes executables and vendor units but intentionally retains data,
configuration, credentials, and the `nando` account. Disable enabled instances
manually before removal. User-manager enablement links belong to each user and
are not manipulated by package scripts.
