# Configuration reference

`nando`, `nando-fuse`, and `nando-webdav` share one TOML configuration format. The default file is `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`; a missing implicit default is allowed. An explicit `--config` path must exist. Command-line options take precedence over TOML values.

Configuration file selection and repository selection are separate. The repository comes from `--repository`, then `[repository].path`, then `${XDG_DATA_HOME:-~/.local/share}/nando`. The daemon endpoint comes from `--daemon-listen`, then `[daemon].listen`, then a Unix socket derived from the repository path.

For configuration by task, see [Configure nando for a task](../setup/configuration). The complete commented sample at [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) documents optional settings. Repository backend and URI are selected by `init` and stored with the repository, rather than selected in this shared user config.
