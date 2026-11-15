# CLI

全コマンドは共通して `--config`、`--repository`、`--daemon-listen`、`--daemon-token-file`、`--daemon-ca-cert` を受け取ります。global flag は subcommand より前に置きます。

```sh
nando --repository "$HOME/nando-data" ls /
nando --repository "$HOME/nando-data" put ./photo.jpg /photo.jpg
nando --repository "$HOME/nando-data" stat /photo.jpg
nando --repository "$HOME/nando-data" cat /photo.jpg
nando --repository "$HOME/nando-data" get /photo.jpg ./photo-copy.jpg
nando --repository "$HOME/nando-data" mv /photo.jpg /original.jpg
nando --repository "$HOME/nando-data" rm /original.jpg
```

`nando daemon` は daemon を起動します。`nando doctor` は repository health check、`nando gc` は GC、`nando recovery list|retry|discard` は未完了 write session を扱います。`nando admin migrate` は現在の schema を検証します。全コマンド一覧は [CLI reference](../reference/cli) を参照してください。
