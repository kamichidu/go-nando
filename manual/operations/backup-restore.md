# Backup と restore

Backup は operator が行います。repository を一貫した単位として保存してください。詳細な境界と cold backup 手順は [既存の backup/restore 運用設計](https://github.com/kamichidu/go-nando/blob/main/docs/backup-restore.adoc)が正本です。

要点は次の通りです。

1. FUSE、WebDAV、他 client の書き込みを止めます。
2. daemon を正常停止し、repository に書き込む process がないことを確認します。
3. repository directory 全体を一つの consistent set として保存します。SQLite sidecar、`coordination.db`、`objects/`、`repository.json`、`temp/` を含めます。
4. restore も daemon/client 停止中に空の destination へ全体を戻し、owner、mode、config、credential を別途確認します。
5. daemon を起動し、client を戻す前に `nando doctor` と `nando recovery list` を確認します。

Remote queue の drain 完了は必要条件ではありません。pending obligation と Local object を一緒に保存してください。
