# Remote 非同期配送

Remote mode では完全な content をまず Local に commit し、その Entry と配送 obligation を metadata transaction で確定します。daemon scheduler が idle interval、periodic interval、最大配送遅延と parallelism に従って、repository ごとに選択された一つの GCS または S3-compatible backend へ配送します。

CLI の書き込み成功は remote durability の完了を意味しません。remote が利用できない間も配送 obligation は保持され、daemon が後で retry します。daemon を停止・移動する場合は local repository と未配送データを保持してください。

scheduler の4項目は daemon TOML の `[remote]` に置きます。認証は daemon account が使う GCS ADC または AWS SDK credential chain から供給します。配送状況を表示する queue-drain CLI は実装されていません。
