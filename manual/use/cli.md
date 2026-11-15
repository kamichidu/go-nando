# Use the CLI

The CLI is one client of the running daemon. Global options such as `--config` and `--repository` go before the subcommand. If you use the shared XDG defaults, you can omit both:

```sh
nando ls /
nando put ./photo.jpg /photo.jpg
nando stat /photo.jpg
nando cat /photo.jpg
nando get /photo.jpg ./photo-copy.jpg
nando mv /photo.jpg /original.jpg
nando rm /original.jpg
```

For a normal workflow, initialize once with `nando init`, keep `nando daemon` running (or use the [systemd service](../setup/systemd)), then use commands such as `put`, `ls`, and `get`. `nando init` creates the repository directly and does not connect to the daemon. All other repository selection must match the daemon's repository and endpoint.

Commands also include `doctor`, `gc`, `recovery list|retry|discard`, and `admin recover`. `nando sync` is present but not implemented. The [CLI reference](../reference/cli) summarizes commands; run `nando --help` or a command's `--help` for its supported options.

The `fuse` and `webdav` subcommands start separate adapter executables. See [FUSE](./fuse) and [WebDAV](./webdav) for workflows.
