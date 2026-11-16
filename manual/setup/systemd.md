# Run nando with systemd

systemd is the usual way to keep a daemon running on Debian or Ubuntu Linux. It starts, supervises, and stops the repository-owning `nando daemon` process. It does not replace the daemon and does not start FUSE or WebDAV adapters.

The Debian package installs a system template and a user-manager template. They run with different identities and paths:

| Unit | Process identity | Config selected by the unit | Repository and writable state |
| --- | --- | --- | --- |
| `nando@<instance>.service` | Dedicated non-login `nando` account | `/etc/nando/<instance>.toml` | Set the repository path in that file; package state is under `/var/lib/nando/<instance>` and runtime socket directory is `/run/nando-<instance>` |
| `nando@<instance>.service` in the user manager | The logged-in user | `%E/nando/<instance>.toml`; `%E` is the manager config root (`$XDG_CONFIG_HOME` for the user manager) | Configured repository path, or the user's XDG data default; the unit permits writes under `~/.local/share/nando` and its runtime directory |

The two units share a template name but belong to different systemd managers. The rest of this page documents the system service, which is the package's usual multi-user service setup. The Unix socket configured in this system-service procedure is owner-only: run its CLI and adapters as the `nando` account with the same instance config. A normal login account cannot use that socket directly; the examples use `sudo -u nando` so the client process has the service account identity. The daemon also supports a separately configured TCP endpoint with TLS and bearer authentication, but this procedure uses the local Unix socket.

For personal use, initialize and run a repository as your logged-in account, following the [quick start](../getting-started/quickstart). Those commands use your account's XDG data directory and socket. They do not automatically select the system service repository. The intended future behavior of naturally selecting a system-standard repository with an explicit switch to personal storage is not implemented; repository discovery and cross-account socket access are separate unresolved implementation requirements.

## Configure and start a system instance

This example uses the instance name `archive` and repository path `/var/lib/nando/archive`.

1. Install the package.

   ```sh
   sudo dpkg -i ./go-nando_*_linux_amd64.deb
   ```

   The package maintainer script reloads systemd unit definitions. It does not enable or start services or initialize repositories.

2. Create the config directory and copy the packaged system sample.

   ```sh
   sudo install -d -o root -g nando -m 0750 /etc/nando
   sudo install -o root -g nando -m 0640 \
     /usr/share/doc/go-nando/config.system.example.toml \
     /etc/nando/archive.toml
   ```

3. In `/etc/nando/archive.toml`, set the repository path to `/var/lib/nando/archive` and the daemon listener to `unix:///run/nando-archive/control.sock`. For Remote mode, configure all scheduler values. Provision GCS ADC or AWS credentials for the `nando` account outside the package.
4. Create the repository parent directory and assign it to the service account. Initialization runs as `nando` and creates the repository directory beneath this parent.

   ```sh
   sudo install -d -o nando -g nando -m 0700 /var/lib/nando
   ```

5. Initialize the repository as the service account.

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml init
   ```

6. Enable and start the instance.

   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now nando@archive.service
   sudo systemctl status nando@archive.service
   ```

7. Check daemon connectivity and repository integrity as the service account. A successful `doctor` call does not report remote delivery progress.

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml doctor
   ```

Run other repository commands as `nando` with the same config, for example `sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml ls /`. The daemon reads `/etc/nando/archive.toml`, stores repository data under `/var/lib/nando/archive`, and creates its control socket under `/run/nando-archive`. The config file itself is owned by root and readable by group `nando`. Do not run an ordinary login-user client against this owner-only socket; cross-account authorization is not implemented. `doctor` checks that the daemon responds and its repository integrity check passes; it does not report the resolved path or confirm that remote deliveries have completed.

## Use FUSE or WebDAV with the instance

Adapters run as separate processes. Start the daemon service first, then start an adapter with the same instance config and operating-system account, for example:

```sh
sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml fuse --fuse-mountpoint /path/to/mount
sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml webdav
```

The mountpoint and host FUSE policy need their own setup. WebDAV remains unauthenticated loopback HTTP. systemd does not start either adapter automatically.

## Upgrade and removal

A package upgrade does not restart a running daemon. Review release notes and restart the instance during a maintenance window. Before removing the package, manually disable enabled services. Repository data, configuration, and credentials are retained.
