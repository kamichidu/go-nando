# Configure nando for a task

`nando`, `nando-fuse`, and `nando-webdav` use one shared TOML file. Its default path is `${XDG_CONFIG_HOME:-~/.config}/nando/config.toml`; the file may be absent. The repository has a separate default, `${XDG_DATA_HOME:-~/.local/share}/nando`.

Command-line options override their corresponding TOML settings. Use `--config` to select another TOML file. Repository selection follows this order:

1. Explicit `--repository` option.
2. Configured `[repository].path`.
3. XDG data default.

Selecting a config file does not discover a repository automatically.

The commented [sample configuration](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml) shows the optional settings without requiring you to copy every setting.

## Use the default personal repository

For a first local repository, no TOML file is required:

```sh
nando init
nando daemon
```

For everyday file access, start the daemon and then use [FUSE](../use/fuse) or [WebDAV](../use/webdav) with the same account and repository defaults. `init` creates the repository directly; the daemon opens it for clients. Follow the [Quick Start](../getting-started/quickstart). CLI commands remain available for administration and diagnostics.

## Choose a different repository

To keep a repository somewhere else, either pass `--repository /absolute/path/to/repository` consistently to `init`, `daemon`, the CLI, and each adapter, or set a persistent `[repository].path` in the TOML file:

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

Configure credentials for the daemon account, then initialize with the backend and URI using the [GCS](./gcs) or [S3-compatible storage](./s3) procedure. Start the daemon with that config.

`nando doctor` confirms that the selected daemon responds and that its repository integrity check passes. It does not report delivery progress. No command reports when the remote delivery queue has drained, and a successful write does not confirm remote durability.

## Set a Remote repository's Local capacity limit

`--max-local-bytes` limits Local object storage for a Remote-mode repository; capacity GC can evict content after remote delivery. It does not limit a Local-mode repository, where local objects are the durable authority. Set it when initializing the repository; `0` disables capacity eviction. For example:

```sh
nando init \
  --delivery-mode remote \
  --remote-backend gcs \
  --remote-uri gs://my-bucket/nando/ \
  --max-local-bytes 10737418240
```

Configure the remote scheduler and credentials as described above before starting the daemon. There is no CLI command to display or verify the stored capacity limit. `nando doctor` checks repository integrity but does not report this setting. Review [garbage collection](../operations/gc) before relying on capacity management.

## Access files through FUSE or WebDAV

Use an adapter in this order:

1. Install the relevant adapter, initialize the repository, and start its daemon.
2. Launch the adapter as a separate process with the same repository selection and daemon endpoint.

FUSE requires a mountpoint and host FUSE support. WebDAV listens on unauthenticated loopback HTTP. Follow the [FUSE](../use/fuse) or [WebDAV](../use/webdav) procedure to start and stop each adapter. [systemd service operation](./systemd) explains the service-account boundary.

## Run more than one instance

The current way to select another instance is to give it a distinct config file and repository path, then pass `--config` to every process that uses it. For system services, the instance name selects `/etc/nando/<instance>.toml`; see [systemd setup](./systemd). This selects a particular instance explicitly; nando does not discover a system-wide “standard” repository automatically.

## Check daemon connectivity and repository health

Run `nando doctor` with the same options and operating-system account as the daemon. A successful result means the selected daemon responded and its repository integrity check passed. It does not:

- Print the resolved repository path.
- Report the configured Local capacity limit.
- Show whether remote delivery has drained.

For a system service, use its config and account as shown in the [systemd procedure](./systemd). The complete commented example is available at [`samples/config.toml`](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml); the [configuration reference](../reference/configuration) summarizes the shared format.
