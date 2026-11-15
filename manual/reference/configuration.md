# Configuration reference

`nando`、`nando-fuse`、`nando-webdav` は共通の per-user TOML を読みます。system service は `/etc/nando/<instance>.toml` のように `--config` で明示します。

| TOML table | Supported keys |
| --- | --- |
| `[repository]` | `path` |
| `[logging]` | `verbose` |
| `[remote]` | `sync_idle_interval`, `sync_periodic_interval`, `max_delivery_delay`, `sync_parallelism` |
| `[remote.s3]` | `endpoint`, `region` |
| `[daemon]` | `listen`, `token`, `token_from_file`, `token_from_env`, `ca_cert`, `tls_cert`, `tls_key` |
| `[fuse]` | `mountpoint`, `mount_options` |
| `[webdav]` | `listen` |
| `[recovery]` | `directory` |

`[daemon]` の token source は一つだけ指定します。TCP listener は TLS と bearer token を要求します。`[remote.s3]` は S3 repository にのみ使い、credential field はありません。remote URI/backend は user TOML に書かず、`init` の引数で repository に保存します。

完全なコメント付き例は [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) を参照してください。CLI option が TOML より優先されます。
