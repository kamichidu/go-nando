# Google Cloud Storage

GCS uses `remote_backend = gcs` and Google Application Default Credentials
(ADC). Nando creates the Cloud Storage client in the daemon process, which
uses the standard ADC lookup order. Credentials are not stored in the
repository. See Google's documentation for
[ADC](https://cloud.google.com/docs/authentication/application-default-credentials)
and [providing credentials](https://cloud.google.com/docs/authentication/provide-credentials-adc).

The daemon's operating-system user and environment determine which ADC sources
it can access. For a systemd service, the ADC file or runtime identity must be
available to the service process; a login user's ADC configuration is not
automatically available to a system service. Configure environment variables
or credential-file access for the service account as appropriate, and see the
[systemd setup guide](./systemd).

For a foreground daemon using a credential configuration or JSON key file,
set `GOOGLE_APPLICATION_CREDENTIALS` to the file path:

```sh
GOOGLE_APPLICATION_CREDENTIALS="$HOME/.config/gcloud/application_default_credentials.json" \
  nando --config "$HOME/.config/nando/gcs.toml" \
  --repository "$HOME/nando-data" daemon
```

This path is an example ADC source; ADC also supports other methods described
in Google's documentation. Keep credential files protected and readable by
the account running the daemon.

## Configure a repository

One way to authenticate during development is to create ADC for the user who
will run the daemon:

```sh
gcloud auth application-default login
```

This is one ADC option, not a special Nando setting. Other environments can
use `GOOGLE_APPLICATION_CREDENTIALS` to point to a credential configuration
or JSON key file, or provide an attached runtime identity such as a service
account on Google Cloud. Use the identity mechanism appropriate to the
environment and grant it the bucket permissions required by the application.

Save the scheduler settings to a TOML file. The explicit `--config` path must
exist before Nando starts:

```sh
mkdir -p "$HOME/.config/nando"
cat > "$HOME/.config/nando/gcs.toml" <<'EOF'
[remote]
sync_idle_interval = "30s"
sync_periodic_interval = "5m"
max_delivery_delay = "30m"
sync_parallelism = 2
EOF
```

Initialize the repository with the config and an explicit repository path:

```sh
nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" init \
  --delivery-mode remote \
  --remote-backend gcs \
  --remote-uri gs://my-bucket/nando/
```

Create the bucket and grant the selected identity the required permissions.
Start the daemon under the same account/environment that has ADC configured:

```sh
nando --config "$HOME/.config/nando/gcs.toml" \
  --repository "$HOME/nando-data" daemon
```

In another terminal, start either FUSE or WebDAV with the same config and
repository selection:

```sh
mkdir -p "$HOME/nando-mount"
nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" \
  fuse --fuse-mountpoint "$HOME/nando-mount"
```

Or use WebDAV:

```sh
nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" webdav
```

The GCS backend and URI are stored in the repository; credentials and scheduler
settings are used at runtime. See [FUSE](../use/fuse) or
[WebDAV](../use/webdav) for adapter behavior.
