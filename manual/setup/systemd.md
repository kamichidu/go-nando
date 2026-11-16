# Run nando with systemd

systemd is the usual way to keep a daemon running on Debian or Ubuntu Linux. It starts, supervises, and stops the repository-owning `nando daemon` process. It does not replace the daemon and does not start FUSE or WebDAV adapters.

The Debian package installs a system template and a user-manager template. They run with different identities and paths:

| Unit | Process identity | Config selected by the unit | Repository and writable state |
| --- | --- | --- | --- |
| `nando@<instance>.service` | Dedicated non-login `nando` account | `/etc/nando/<instance>.toml` | Set the repository path in that file; package state is under `/var/lib/nando/<instance>` and runtime socket directory is `/run/nando-<instance>` |
| `nando@<instance>.service` in the user manager | The logged-in user | `%E/nando/<instance>.toml`; `%E` is the manager config root (`$XDG_CONFIG_HOME` for the user manager) | Configured repository path, or the user's XDG data default; the unit permits writes under `~/.local/share/nando` and its runtime directory |

The two units share a template name but belong to different systemd managers. The rest of this page covers the system service, the package's usual multi-user setup.

The system-service procedure configures an owner-only Unix socket. Run its CLI and adapters as the `nando` account with the same instance config; a normal login account cannot use the socket directly. Examples use `sudo -u nando` to run clients as the service account. Although the daemon also supports a separately configured TCP endpoint with TLS and bearer authentication, this procedure uses the local Unix socket.

For personal use, initialize and run a repository as your logged-in account using the [quick start](../getting-started/quickstart). Those commands use your account's XDG data directory and socket; they do not automatically select the system-service repository.

Automatic selection of a system-standard repository, an explicit switch to personal storage, and cross-account socket access are not implemented.

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
4. If the daemon needs cloud credentials, provide its environment in a systemd drop-in. The system unit has `ProtectHome=yes`, so the service cannot read the invoking user's `~/.aws` files or home-directory ADC files. For AWS, install the profile's config and credentials files at system paths accessible to the service, for example `/etc/nando/aws-config` and `/etc/nando/aws-credentials`, and set their ownership to `root:nando` with mode `0640`. The profile name must exist in the appropriate file, and the `nando` account must be able to read both files. Create `/etc/systemd/system/nando@archive.service.d/credentials.conf` with:

   ```ini
   [Service]
   Environment="AWS_PROFILE=archive"
   Environment="AWS_CONFIG_FILE=/etc/nando/aws-config"
   Environment="AWS_SHARED_CREDENTIALS_FILE=/etc/nando/aws-credentials"
   ```

   For GCS, use this alternative drop-in setting instead of the AWS variables:

   ```ini
   [Service]
   Environment="GOOGLE_APPLICATION_CREDENTIALS=/etc/nando/credentials/gcs.json"
   ```

   Place the credential file outside home directories and make it readable by the `nando` account, for example root-owned, group `nando`, mode `0640`. Set only the variables for the backend in use, then run `sudo systemctl daemon-reload` and restart the service after it has been started. The environment variables provide file paths, not credentials; keep the files out of the instance TOML and grant only the selected service identity the required cloud permissions. These paths are read-only inputs under the unit's `ProtectSystem=strict` filesystem protection; do not assume arbitrary service writes are allowed. An `EnvironmentFile=` can hold environment assignments when root-owned and tightly permissioned, but it does not replace securing credential files.
5. Create the repository parent directory and assign it to the service account. Initialization runs as `nando` and creates the repository directory beneath this parent.

   ```sh
   sudo install -d -o nando -g nando -m 0700 /var/lib/nando
   ```

6. Initialize the repository as the service account.

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml init
   ```

7. Enable and start the instance.

   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now nando@archive.service
   sudo systemctl status nando@archive.service
   ```

8. Check daemon connectivity and repository integrity as the service account. A successful `doctor` call does not report remote delivery progress.

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml doctor
   ```

Run repository commands as `nando` with the same config. For example:

```sh
sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml ls /
```

The daemon reads `/etc/nando/archive.toml`, stores data under `/var/lib/nando/archive`, and creates its control socket under `/run/nando-archive`. The config file is owned by root and readable by group `nando`. Cross-account authorization is not implemented, so do not run an ordinary login-user client against the owner-only socket.

`doctor` checks that the daemon responds and its repository integrity check passes. It does not report the resolved path or confirm that remote deliveries have completed.

## Use FUSE or WebDAV with the instance

Adapters run as separate processes. Start the daemon service first, then start an adapter with the same instance config and operating-system account, for example:

```sh
sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml fuse --fuse-mountpoint /path/to/mount
sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml webdav
```

The mountpoint and host FUSE policy need their own setup. WebDAV remains unauthenticated loopback HTTP. systemd does not start either adapter automatically.

## Upgrade and removal

A package upgrade does not restart a running daemon. Review release notes and restart the instance during a maintenance window. Before removing the package, manually disable enabled services. Repository data, configuration, and credentials are retained.
