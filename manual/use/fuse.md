# FUSE: mount, use, and unmount

`nando-fuse` is a separate filesystem adapter. It connects to the daemon and presents the repository through a mountpoint, so ordinary filesystem programs can work with repository files. On a Debian package, invoke it through `nando fuse`.

## Mount

Initialize the repository and start its daemon first. Use the same operating-system account, config, and repository selection for the adapter:

```sh
mkdir -p "$HOME/nando-mount"
nando fuse --fuse-mountpoint "$HOME/nando-mount"
```

With the shared XDG defaults, no explicit repository path is needed. If the daemon uses another repository or config, pass the matching `--repository` or `--config`. A system-service repository requires running the adapter as the `nando` service account with that service's config; see [systemd setup](../setup/systemd).

FUSE requires a supported platform and FUSE runtime. The mountpoint must be accessible to the account running the adapter. Optional mount settings are `allow_other` and `default_permissions`; both are disabled by default. `allow_other` may require `user_allow_other` in `/etc/fuse.conf`.

## Use the mounted filesystem

The current filesystem operation surface is:

- **Implemented:** lookup and attributes, directory listing, file open/read/write, file creation, directory creation, unlink, directory removal, rename, file truncation, and mode changes.
- **Limited:** rename does not replace an existing destination; that operation returns `EEXIST`.
- **Not supported:** hard links are explicitly rejected. This list does not imply support for every POSIX filesystem operation.

These are filesystem operations; they are not WebDAV HTTP methods.

## Write and commit behavior

Writes go to daemon-managed staging. For a linked file, `fsync` synchronizes dirty staging and requests a repository commit; successful return is the synchronous local commit boundary. In Remote mode, delivery to object storage remains asynchronous. When the last writable handle for a still-linked file closes, the adapter also attempts to commit remaining dirty data. FUSE `Flush` is not a commit signal. If a commit fails, the unfinished write is retained for Recovery rather than reported as committed. If a file is unlinked while a writer is open, that handle's staging is not committed back into the namespace; `fsync` only syncs its private staging. Use `fsync` when an application needs a synchronous local commit point; do not rely on close alone for that guarantee.

## Unmount

Press Ctrl-C in the adapter terminal or send SIGTERM to the adapter process. It unmounts the filesystem before exiting. Unmount the adapter before stopping the daemon. The systemd service manages the daemon only and does not start this adapter.
