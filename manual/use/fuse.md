# FUSE

`nando-fuse` is a separate filesystem adapter and requires a FUSE runtime on a supported platform. The Debian package exposes it through `nando fuse`.

```sh
nando fuse --repository "$HOME/nando-data" --fuse-mountpoint "$HOME/nando-mount"
```

Set FUSE mount options with `[fuse].mount_options` or repeat `--fuse-mount-option`. The supported values are `allow_other` and `default_permissions`; both are disabled by default. `allow_other` may also require a host setting in `/etc/fuse.conf`.

Unmount the filesystem before stopping the daemon. Closing a FUSE handle may not provide a synchronous commit boundary; use `fsync` when the caller needs synchronous visibility.
