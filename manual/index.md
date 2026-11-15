# nando

nando is a content-addressed repository for durable files. It stores names and directory structure as metadata and file contents as content-addressed objects.

Select one GCS or S3-compatible object storage backend to enable remote delivery. Writes commit locally first; the daemon then delivers them asynchronously. A successful write does not confirm remote durability.

## Start here

- [Install nando](./getting-started/install)
- [Local quickstart](./getting-started/quickstart)
- [Configure a remote backend](./setup/gcs)
- [Run a systemd system service](./setup/systemd)
- [Back up and restore a repository](./operations/backup-restore)

See the [CLI reference](./reference/cli) and [configuration reference](./reference/configuration) for implemented commands and settings.
