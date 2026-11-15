# 制約

このページは複数箇所に重複しないよう、主要な現在の境界だけをまとめます。

- v1.0.0 より前の repository/configuration/recovery state との互換性は保証しません。
- 一 repository で remote backend は最大一つです。複数 provider replication、failover、tiering はありません。
- remote 配送は非同期で、書き込み成功時に remote durability は保証されません。
- URI prefix は literal です。末尾 `/` を自動補完しません。
- S3 upload は single PutObject で、multipart upload はありません。
- WebDAV は loopback-only の unauthenticated HTTP です。
- system service の socket は専用 service account のみが使えます。複数 OS account の client authorization はありません。
- SQLite schema migration はまだ v1 baseline のみです。将来の immediate-minor migration と downgrade は現行機能ではありません。
- queue-drain 状態を表示する CLI、binary の自動更新、Repository format/backend の自動切替はありません。
- `nando sync` command は存在しますが、同期処理は未実装です。Remote 配送は daemon scheduler が行います。
