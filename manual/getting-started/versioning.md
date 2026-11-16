# Versions and update policy

`v1.0.0` starts compatibility guarantees. Compatibility with repository, configuration, CLI, or Recovery state created before `v1.0.0` is not guaranteed.

| Release type | What to expect |
| --- | --- |
| Patch | Backward-compatible fixes. |
| Minor | Backward-compatible features. |
| Major | May include breaking changes. |

Updates are not installed automatically. Choose when to install a release and restart a running daemon.

## Repository updates

Starting the daemon or running `nando init` does not update an existing repository. Use the explicit `nando admin migrate` command when a release requires a repository update. The current v1 repository format is the only supported format; the command validates it and does not perform a general schema migration. Unsupported repository state is rejected without being rewritten.

Before updating, follow the release instructions and make a consistent backup using the [backup and restore guide](../operations/backup-restore). For the canonical developer design and schema lifecycle, see [`docs/design.ir.yaml`](https://github.com/kamichidu/go-nando/blob/main/docs/design.ir.yaml).
