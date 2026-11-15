# CLI reference

global option は subcommand より前に置きます。`nando --help` と各 command の `--help` で実行ファイルが出す option を確認できます。

| Command | 用途 |
| --- | --- |
| `init` | 新しい repository を初期化 |
| `daemon` | daemon を起動 |
| `ls`, `stat` | namespace の参照 |
| `put`, `get`, `cat` | file の登録・取得・標準出力 |
| `mv`, `rm` | Entry の移動・削除 |
| `doctor`, `gc` | health check、Local object 回収 |
| `sync` | command は存在しますが、現在は not implemented を返します |
| `recovery list|retry|discard` | 保留 write session を確認・処理 |
| `admin recover` | remote object の metadata を復元 |
| `admin migrate` | 現行 schema を検証 |
| `maintenance --interval DURATION` | 定期 maintenance client を実行 |
| `fuse`, `webdav` | 別 adapter executable を起動 |

Remote storage を指定するのは `init` の `--delivery-mode remote`、`--remote-backend gcs|s3`、`--remote-uri URI` です。Capacity GC 上限は `--max-local-bytes` です。全ての実装済み option は help output を参照してください。
