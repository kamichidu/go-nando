# Browse a repository with FUSE

`nando-fuse` is a separate filesystem adapter. It mounts a view of the repository and sends file operations to the daemon. On a Debian package, invoke it through `nando fuse`; the adapter still runs as a separate process from the daemon.

Initialize the repository and start its daemon first. In another terminal, select a mountpoint:

```sh
mkdir -p "$HOME/nando-mount"
nando fuse --fuse-mountpoint "$HOME/nando-mount"
```

With the shared XDG defaults, no explicit repository path is needed. If the daemon uses a different repository or config, pass the matching `--repository` or `--config` to this command too. FUSE requires a supported platform and FUSE runtime. `allow_other` and `default_permissions` are optional mount settings and are disabled by default; `allow_other` may also require `user_allow_other` in `/etc/fuse.conf`.

Unmount the filesystem before stopping the daemon. Closing a FUSE handle may not provide a synchronous commit boundary; use `fsync` when the caller needs synchronous visibility. The [systemd service](../setup/systemd) does not start the adapter automatically.
