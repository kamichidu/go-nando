# Quick start for everyday file access

After [installing nando](./install), choose who owns the repository. This example creates a personal Local-mode repository under the current account's XDG data default, `${XDG_DATA_HOME:-~/.local/share}/nando`.

## Initialize and start the daemon

Initialize the personal repository directly:

```sh
nando init
```

For a quick personal setup or a non-systemd environment, start its daemon in one terminal and leave it running:

```sh
nando daemon
```

For a packaged system repository on Debian or Ubuntu, use the [systemd system-service procedure](../setup/systemd) instead. That procedure initializes and runs a separate repository as the dedicated `nando` account. Do not initialize a personal repository and expect the system service to open it; the repository, config, and operating-system account must match.

## Choose how to use files

Start one adapter in another terminal, using the same account and repository as the daemon. FUSE presents the repository as a mounted filesystem:

```sh
mkdir -p "$HOME/nando-mount"
nando fuse --fuse-mountpoint "$HOME/nando-mount"
```

Use ordinary filesystem programs on `$HOME/nando-mount`. Stop the adapter with Ctrl-C or SIGTERM; it unmounts before exiting. See [FUSE: mount, use, and unmount](../use/fuse) for supported operations and commit behavior.

Alternatively, start WebDAV:

```sh
nando webdav
```

Connect a WebDAV client on the same host that supports the [implemented HTTP methods](../use/webdav#implemented-http-methods) to `http://127.0.0.1:8080/`. Stop the adapter with Ctrl-C or SIGTERM. See [WebDAV: start, use, and stop](../use/webdav) for method semantics and protocol limits. The two adapters are separate ways to access the same daemon-owned repository; run either or both as needed.

For a system-service repository, start the adapter with the instance config and as the `nando` account, as shown in the [systemd procedure](../setup/systemd). The system service does not start adapters automatically.

## Optional CLI smoke check

The CLI is primarily for initialization, administration, diagnostics, and debugging. To verify a personal daemon and repository before mounting or connecting a WebDAV client, open another terminal and run:

```sh
printf 'nando quick start sample\n' > ./photo.jpg
nando put ./photo.jpg /photo.jpg
nando ls /
nando get /photo.jpg ./photo-copy.jpg
```

These commands connect to the daemon; they are an optional smoke check, not the normal file-access workflow. The same repository and endpoint selection must be used by the CLI, daemon, and adapter. For administrative commands, see [CLI administration](../use/cli).
