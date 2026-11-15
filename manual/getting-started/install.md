# インストール

## Debian / Ubuntu

[GitHub Releases](https://github.com/kamichidu/go-nando/releases) から Linux amd64 用 `.deb` を取得し、dpkg でインストールします。

```sh
sudo dpkg -i ./go-nando_*_linux_amd64.deb
```

パッケージは `nando` 本体を `/opt/nando/bin`、adapter を `/opt/nando/libexec` に配置し、`/usr/bin/nando` を提供します。daemon の起動・有効化や repository の初期化は自動では行いません。次の [ローカル quickstart](./quickstart) または [systemd system service](../setup/systemd) に従ってください。

## アーカイブ

Linux、macOS、Windows の対応アーキテクチャ用アーカイブは Releases に掲載します。展開後、実行ファイルがあるディレクトリを `PATH` に追加してください。FUSE と WebDAV adapter は対象 platform の archive に含まれる場合のみ利用できます。

## ソースからのビルド

開発 checkout の `go.work` を使い、Go 1.25.1 以降で次を実行します。

```sh
go install ./cmd/nando
go install ./adapters/fuse/cmd/nando-fuse
go install ./adapters/webdav/cmd/nando-webdav
```

一般利用では GitHub Releases または Debian package を使ってください。nested Go module は checkout の workspace に依存します。
