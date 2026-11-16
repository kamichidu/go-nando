# Initialize a repository

For a personal repository under the current account's XDG data default (`${XDG_DATA_HOME:-~/.local/share}/nando`), initialize without an explicit path:

```sh
nando init
```

To choose another location, pass the same repository path to initialization, the daemon, and the FUSE or WebDAV adapter:

```sh
nando --repository "$HOME/nando-data" init
```

`init` creates repository metadata and local storage directly; it does not need a running daemon. To use a packaged system service, follow the [systemd procedure](./systemd), which selects a separate service-account repository and initializes it as `nando`.

## Select remote delivery

To use remote delivery, select one backend and URI during initialization. The choice is saved in repository settings and cannot be changed by normal startup. Remote mode still commits content locally first and delivers it asynchronously.

```sh
nando init \
  --delivery-mode remote \
  --remote-backend gcs \
  --remote-uri gs://my-bucket/nando/
```

For S3-compatible storage, use `--remote-backend s3` and an `s3://bucket[/prefix]` URI. A repository selects one `remote_backend`; the URI prefix is used literally in object keys, so a trailing `/` is not added automatically. See [GCS](./gcs) or [S3-compatible storage](./s3) for connection setup and required daemon scheduler settings.

After initialization, start the [daemon](../operations/daemon), then use the [FUSE](../use/fuse) or [WebDAV](../use/webdav) adapter for everyday file access. The CLI is available for [administration and diagnostics](../use/cli).
