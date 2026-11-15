# systemd system service

The Debian package installs the `nando@.service` system template and creates a dedicated non-login `nando` account. The system service runs as this account. Its socket is owner-only, so CLI, FUSE, and WebDAV clients for the instance must run as `nando` and use the same endpoint. The socket is not shared with a regular login account. The package also includes a `nando@.service` template for the user manager.

## Configure an instance

This example uses the instance name `archive` and repository path `/var/lib/nando/archive`.

1. Install the package.

   ```sh
   sudo dpkg -i ./go-nando_*_linux_amd64.deb
   ```

   The package maintainer script reloads systemd unit definitions. It does not enable or start services or initialize repositories.

2. Create the config directory and copy the packaged sample.

   ```sh
   sudo install -d -o root -g nando -m 0750 /etc/nando
   sudo install -o root -g nando -m 0640 \
     /usr/share/doc/go-nando/config.system.example.toml \
     /etc/nando/archive.toml
   ```

3. In `/etc/nando/archive.toml`, set the repository path to `/var/lib/nando/archive` and the daemon listener to `unix:///run/nando-archive/control.sock`. For Remote mode, configure all scheduler values. Provision GCS ADC or AWS credentials for the `nando` account outside the package.
4. Create the repository parent directory and assign it to the service account. Repository initialization runs as `nando` and creates the repository directory beneath this parent.

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

7. Verify that the daemon accepts a request from the same account.

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml doctor
   ```

Run other repository commands as `nando` with the same config. Access from another operating-system account requires an authorization design; relaxing socket permissions is unsupported.

## FUSE and WebDAV

Adapters run as separate processes. Start the daemon first, then start each adapter with the same config and operating-system account. Configure FUSE mountpoint ownership and host FUSE policy separately. The system service does not start adapters automatically.

## Upgrade and removal

A package upgrade does not restart a running daemon. Review release notes and restart the instance during a maintenance window. Before removing the package, manually disable enabled services. Repository data, configuration, and credentials are retained.
