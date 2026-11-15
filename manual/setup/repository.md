# Repository の初期化

Repository の置き場所を決め、新しい directory を `init` に渡します。

```sh
nando --repository "$HOME/nando-data" init
```

Remote 配送を使う場合は、初期化時に backend と URI を repository 設定へ保存します。既存 repository の backend や URI を通常起動時に切り替える機能はありません。

```sh
nando --repository "$HOME/nando-data" init \
  --delivery-mode remote \
  --remote-backend gcs \
  --remote-uri gs://my-bucket/nando/
```

S3-compatible storage では `--remote-backend s3` と `s3://bucket[/prefix]` を使います。`remote_backend` は repository ごとに一つ選択します。URI prefix の末尾 `/` はそのまま object key に反映され、自動補完されません。詳しくは [GCS](./gcs) または [S3 / Wasabi](./s3) を参照してください。

日常運用では daemon を起動します。systemd 管理を使う場合は [system service の手順](./systemd)を参照してください。
