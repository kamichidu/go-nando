# Configure nando for a task

`nando`, `nando-fuse`, and `nando-webdav` use one shared TOML file. By default it is `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`; a missing default file is allowed. The repository defaults independently to `${XDG_DATA_HOME:-~/.local/share}/nando`. A command-line option takes precedence over the corresponding TOML setting, and `--config` selects a different TOML file. Selecting a config file is not repository discovery: repository selection follows the explicit option, configured path, then data default.

The commented [sample configuration](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) shows the optional settings without requiring you to copy every setting.

## Use the default personal repository

For a first local repository, no TOML file is required:

```sh
nando init
nando daemon
```

Run CLI operations in another terminal. `init` creates the repository directly; `daemon` opens it for normal use. See the [quick start](../getting-started/quickstart).

## Choose a different repository

To keep a repository somewhere else, either pass `--repository /absolute/path/to/repository` consistently to `init`, `daemon`, and each CLI operation, or set a persistent `[repository].path` in the TOML file:

```toml
[repository]
path = "/srv/nando/archive"
```

Initialize that path once with `nando init`, then start the daemon using the same config. The process account must be able to access the repository directory.

## Deliver content to a remote backend

Remote selection is saved in repository settings when the repository is initialized. It is not selected by the TOML file. For either provider, the daemon config needs all four positive scheduler values:

```toml
[remote]
sync_idle_interval = "30s"
sync_periodic_interval = "5m"
max_delivery_delay = "30m"
sync_parallelism = 2
```

Configure credentials for the account that runs the daemon, then initialize with the backend and URI as shown in the provider-specific [GCS](./gcs) or [S3 / Wasabi](./s3) procedure. Start the daemon with that config and use `nando doctor` with the same account and config to confirm it responds. There is no command to report when delivery has drained; a successful write is not confirmation of remote durability.

## Set a Local capacity limit

Set the limit at repository initialization with `--max-local-bytes`; `0` disables capacity eviction. This is a repository setting, not a TOML setting. For example:

```sh
nando init --max-local-bytes 10737418240
```

Initialize the repository with that option; it is not changed by a TOML setting. Use `nando doctor` to confirm access to the initialized repository. It does not report the configured limit. Review [garbage collection](../operations/gc) before relying on capacity management.

## Access files through FUSE or WebDAV

Install the relevant adapter, initialize the repository, and start its daemon. Then launch the adapter as a separate process using the same repository selection and daemon endpoint. FUSE requires a mountpoint and host FUSE support; WebDAV currently listens on loopback HTTP and has no authentication. Use the [FUSE](../use/fuse) or [WebDAV](../use/webdav) procedure to start and stop each entry point. See [FUSE](../use/fuse), [WebDAV](../use/webdav), and [systemd service operation](./systemd).

## Run more than one instance

The current way to select another instance is to give it a distinct config file and repository path, then pass `--config` to every process that uses it. For system services, the instance name selects `/etc/nando/<instance>.toml`; see [systemd setup](./systemd). This selects a particular instance explicitly; nando does not discover a system-wide “standard” repository automatically.

## Confirm the selected repository

Run `nando doctor` with the same options and operating-system account used by the daemon. For service instances, use the service's config and account, as shown in the [systemd procedure](./systemd). The complete commented example is available at [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml); the [configuration reference](../reference/configuration) summarizes supported tables.
