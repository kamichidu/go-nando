# Google Cloud Storage

GCS uses `remote_backend = gcs` and Google Application Default Credentials (ADC). Credentials are not stored in the repository.

1. Configure ADC for the account that will run nando. For local development, use `gcloud auth application-default login`. In production, use workload identity or provision credentials through your environment's approved mechanism.
2. Save the scheduler settings to a TOML file. The explicit `--config` path must exist before nando starts.

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

3. Initialize the repository with this config and an explicit repository path:

   ```sh
   nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" init \
     --delivery-mode remote \
     --remote-backend gcs \
     --remote-uri gs://my-bucket/nando/
   ```

4. Create the bucket and configure IAM permissions in Google Cloud. In one terminal, start the daemon as the ADC-configured account and leave it running:

   ```sh
   nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" daemon
   ```

5. In another terminal, start either FUSE or WebDAV with the same config and repository selection:

   ```sh
   mkdir -p "$HOME/nando-mount"
   nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" fuse --fuse-mountpoint "$HOME/nando-mount"
   # Or: nando --config "$HOME/.config/nando/gcs.toml" --repository "$HOME/nando-data" webdav
   ```

See [FUSE](../use/fuse) or [WebDAV](../use/webdav) for adapter behavior. The GCS backend and URI are stored in the repository; credentials and scheduler settings are used at runtime.
