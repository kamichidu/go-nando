# daemon 運用

一つの daemon process が repository を開き、CLI・FUSE・WebDAV の client request と maintenance を処理します。同じ repository に複数 daemon instance を起動しないでください。

systemd system service は [systemd 手順](../setup/systemd)を参照してください。前景で実行する場合は以下です。

```sh
nando --repository "$HOME/nando-data" daemon
```

終了時は SIGTERM を使って正常停止します。未完了 write session は Recovery の対象になります。remote 配送 scheduler は daemon が動いている間に実行されます。
