# What happens to your files

nando keeps your files in a repository that you can use through FUSE or
WebDAV. The command-line tools help initialize and maintain that repository.
See [FUSE](../use/fuse), [WebDAV](../use/webdav), and the [CLI reference](../reference/cli).

## When a change is committed

nando prepares the complete new file content before making it the current
version. Until the change is committed, the previously committed version
remains current. A successful local commit means the new version is available
from the repository; it does not necessarily mean that a remote copy exists.

In Local mode, the repository's local copy is the authoritative copy. In Remote
mode, nando first commits the content locally and records the work needed to
deliver it to the selected remote storage. Delivery runs later in the
background. A successful local commit or FUSE `fsync` is not confirmation of
remote delivery. Keep the local repository available until the selected remote
service has accepted the content; there is no CLI command that reports when the
delivery queue has drained. See [remote delivery](../use/remote-delivery).

## FUSE `fsync` and close

For a file opened through FUSE, a successful `fsync` is the synchronous point
at which dirty content is committed locally. Closing the last writable handle
also asks nando to commit remaining changes, but on Linux the final-close work
may finish after the application's `close` call returns. `Flush` is not a
commit signal. Applications that require a synchronous local commit should
call `fsync` and check its result. See [FUSE write and commit behavior](../use/fuse#write-and-commit-behavior).

An unlinked file is no longer part of the repository namespace. Existing
handles may continue to use their retained data, but changes through a handle
to an unlinked file are not committed back into the namespace.

## Power-loss limits

nando syncs staged file contents before publishing them. On Unix-like systems,
it also syncs the destination directory and reports directory-sync errors. The
result still depends on the operating system, filesystem, storage device, and
any intermediate cache honoring successful flush requests. If one of those
layers reports success before data is stable, nando cannot guarantee that a
recent commit survives power loss or hardware failure.

On Windows, nando requests a best-effort write-through move when publishing an
object. This does not guarantee that the published name survives sudden power
loss. Unit tests cover software-visible publication and retry behavior; they
do not prove recovery after an actual power failure.

For backups, stop clients and the daemon normally and capture the complete
repository as one consistent set. See [backup and restore](../operations/backup-restore).

The [developer design](https://github.com/kamichidu/go-nando/blob/main/docs/design.ir.yaml)
records the detailed storage, commit, and recovery contracts.
