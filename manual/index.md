# nando

nando はローカルを基準にファイルを保管する content-addressed repository です。名前やディレクトリ構造は metadata、ファイル本体は content-addressed object として管理します。

GCS または S3-compatible object storage を一つ選ぶと、書き込みはローカルに commit された後、daemon が remote へ非同期配送します。remote 配送は書き込み完了を待たせないため、remote の耐久化を確認する操作ではありません。

## まず読むページ

- [インストール](./getting-started/install)
- [ローカル quickstart](./getting-started/quickstart)
- [Remote backend の設定](./setup/gcs)
- [systemd system service](./setup/systemd)
- [バックアップと復元](./operations/backup-restore)

利用可能なコマンドと設定は [CLI reference](./reference/cli) と [configuration reference](./reference/configuration) を参照してください。
