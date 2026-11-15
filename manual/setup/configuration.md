# 設定ファイル

3つの実行ファイル `nando`、`nando-fuse`、`nando-webdav` は同じ user TOML を読みます。既定位置は `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`、data directory は `${XDG_DATA_HOME:-~/.local/share}/nando` です。`--config` は設定ファイル、`--repository` は repository を明示します。CLI flag は TOML より優先します。

完全な設定例は [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) にあります。未設定項目は通常省略します。

```toml
[repository]
path = "/home/alex/nando-data"

[logging]
verbose = false

[remote]
sync_idle_interval = "30s"
sync_periodic_interval = "5m"
max_delivery_delay = "30m"
sync_parallelism = 2
```

remote repository は `[remote]` の scheduler 設定をすべて必要とします。S3 のみ `[remote.s3]` に endpoint と region を設定できます。remote URI と backend の選択は user TOML ではなく `init` 時に repository の `repository.json` へ保存します。全項目は [configuration reference](../reference/configuration) を参照してください。
