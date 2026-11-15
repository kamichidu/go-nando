# Google Cloud Storage

GCS uses `remote_backend = gcs` and Google Application Default Credentials (ADC). Credentials are not stored in the repository.

1. Configure ADC for the account that runs the daemon. For local development, use `gcloud auth application-default login`. In production, use workload identity or provision credentials through your environment's approved mechanism.
2. Initialize the repository.

   ```sh
   nando --repository "$HOME/nando-data" init \
     --delivery-mode remote \
     --remote-backend gcs \
     --remote-uri gs://my-bucket/nando/
   ```

3. Add all four scheduler settings to the daemon TOML file and start the daemon.

   ```toml
   [remote]
   sync_idle_interval = "30s"
   sync_periodic_interval = "5m"
   max_delivery_delay = "30m"
   sync_parallelism = 2
   ```

Create the bucket and configure IAM permissions in Google Cloud. Run the daemon under an account with usable ADC credentials.
