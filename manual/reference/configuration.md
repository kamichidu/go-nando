# Configuration reference

`nando`, `nando-fuse`, and `nando-webdav` share one TOML configuration format. The default file is `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`; it may be absent when selected implicitly. An explicit `--config` path must exist. Command-line options take precedence over TOML values.

Configuration file selection and repository selection are separate. Resolution order is:

| Setting | Precedence |
| --- | --- |
| Repository | `--repository`, `[repository].path`, `${XDG_DATA_HOME:-~/.local/share}/nando` |
| Daemon endpoint | `--daemon-listen`, `[daemon].listen`, Unix socket derived from repository path |

For configuration by task, see [Configure nando for a task](../setup/configuration). The complete commented sample at [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) documents optional settings. Repository backend and URI are selected by `init` and stored with the repository, rather than selected in this shared user config.
