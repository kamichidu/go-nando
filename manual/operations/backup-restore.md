# Backup and restore

Operators are responsible for backups. Capture and restore the repository as one consistent set. The detailed boundary and cold backup procedure are maintained in the [backup and restore operations guide](https://github.com/kamichidu/go-nando/blob/main/docs/backup-restore.adoc).

1. Stop FUSE, WebDAV, and other clients that can write to the repository.
2. Stop the daemon normally and verify that no process can write to the repository.
3. While it remains stopped, capture the complete repository directory as one consistent set. Include SQLite sidecars, `coordination.db`, `objects/`, `repository.json`, and `temp/`.
4. Restore the complete set to an empty destination while the daemon and clients are stopped. Check ownership, permissions, configuration, and credentials separately.
5. Start the daemon and run `nando doctor` and `nando recovery list` before resuming clients.

Remote delivery does not need to drain before a backup. Preserve pending delivery obligations and their Local objects together.
