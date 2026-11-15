# Configuration file

`nando`, `nando-fuse`, and `nando-webdav` read the same user TOML file. The default config path is `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`; the default data directory is `${XDG_DATA_HOME:-~/.local/share}/nando`. Use `--config` to select a config file and `--repository` to select a repository. Command-line options take precedence over TOML values.

The complete sample is [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml). Settings that do not apply to your deployment can be omitted.

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

Remote repositories require all four `[remote]` scheduler settings. S3 can also use `[remote.s3]` for `endpoint` and `region`. The remote URI and backend are saved in repository-owned `repository.json` by `init`, not in the user TOML file. See the [configuration reference](../reference/configuration) for all supported keys.
