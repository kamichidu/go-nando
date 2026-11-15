# Google Cloud Storage

GCS は `remote_backend = gcs` に対応します。認証には Google Application Default Credentials (ADC) を使います。credential は repository に保存されません。

1. 実行する daemon account で ADC を準備します。ローカル開発では `gcloud auth application-default login`、本番環境では workload identity または適切に provision した credential を使います。
2. repository を初期化します。

   ```sh
   nando --repository "$HOME/nando-data" init \
     --delivery-mode remote \
     --remote-backend gcs \
     --remote-uri gs://my-bucket/nando/
   ```

3. daemon の TOML に4つの scheduler 設定を追加して daemon を起動します。

   ```toml
   [remote]
   sync_idle_interval = "30s"
   sync_periodic_interval = "5m"
   max_delivery_delay = "30m"
   sync_parallelism = 2
   ```

bucket 作成や IAM 権限の構成は cloud 環境側で行います。GCS credential が使える account で daemon を起動してください。
