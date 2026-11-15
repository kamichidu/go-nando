# Initialize a repository

Choose a repository directory and pass it to `init`:

```sh
nando --repository "$HOME/nando-data" init
```

To use remote delivery, select a backend and URI during initialization. The values are saved in the repository settings. There is no command to change the backend or URI of an existing repository during normal startup.

```sh
nando --repository "$HOME/nando-data" init \
  --delivery-mode remote \
  --remote-backend gcs \
  --remote-uri gs://my-bucket/nando/
```

For S3-compatible storage, use `--remote-backend s3` and an `s3://bucket[/prefix]` URI. Each repository selects one `remote_backend`. The URI prefix is used literally in object keys; a trailing `/` is not added automatically. See [GCS](./gcs) or [S3 / Wasabi](./s3) for provider setup.

Run the daemon for normal use. For systemd, follow the [system service procedure](./systemd).
