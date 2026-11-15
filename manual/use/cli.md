# CLI

All CLI commands accept the common `--config`, `--repository`, `--daemon-listen`, `--daemon-token-file`, and `--daemon-ca-cert` options. Put global options before the subcommand.

```sh
nando --repository "$HOME/nando-data" ls /
nando --repository "$HOME/nando-data" put ./photo.jpg /photo.jpg
nando --repository "$HOME/nando-data" stat /photo.jpg
nando --repository "$HOME/nando-data" cat /photo.jpg
nando --repository "$HOME/nando-data" get /photo.jpg ./photo-copy.jpg
nando --repository "$HOME/nando-data" mv /photo.jpg /original.jpg
nando --repository "$HOME/nando-data" rm /original.jpg
```

`nando daemon` starts the daemon. `nando doctor` checks repository health, `nando gc` runs garbage collection, and `nando recovery list|retry|discard` manages unfinished write sessions. `nando admin migrate` currently validates the schema. See the [CLI reference](../reference/cli) for the command list.
