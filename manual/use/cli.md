# CLI for administration and diagnostics

The CLI is primarily for repository setup, administration, diagnostics, and debugging. For everyday file access, use [FUSE](./fuse) or [WebDAV](./webdav).

`nando init` creates a repository directly. Repository commands such as `doctor`, `gc`, and Recovery operations connect to the running daemon. Put global options such as `--config` and `--repository` before the subcommand; use the same repository and endpoint selection as the daemon.

```sh
nando doctor
nando gc
nando recovery list
nando recovery retry SESSION_ID
nando recovery discard SESSION_ID
```

For a quick connectivity and integrity check, `nando doctor` reports whether the selected daemon responds and its repository integrity check passes. It does not report the resolved repository path, configured Local capacity limit, or remote delivery queue status.

Other commands include `init`, `admin recover`, and `admin migrate`. `nando sync` is present but not implemented. The [CLI reference](../reference/cli) lists commands; run `nando --help` or a command's `--help` for supported options.
