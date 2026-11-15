# Configuration reference

`nando`, `nando-fuse`, and `nando-webdav` read the same per-user TOML file. A system service selects its instance file explicitly, for example with `--config /etc/nando/<instance>.toml`.

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

Choose at most one token source in `[daemon]`. TCP listeners require TLS and a bearer token. `[remote.s3]` applies only to an S3 repository and accepts no credential fields. Set the remote URI and backend through `init`; do not put them in user TOML.

See [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) for the complete commented sample. Command-line options override TOML values.
