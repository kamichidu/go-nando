# Recovery

daemon の中断後に保留された write session は Recovery として確認できます。

```sh
nando recovery list
nando recovery retry SESSION_ID
nando recovery discard SESSION_ID
```

`retry` は session の内容を commit し直し、`discard` はその Recovery staging を破棄します。実行前に session ID、repository path、対象 Entry と失敗理由を確認してください。両コマンドは daemon 接続を必要とします。

Remote storage の object に metadata Entry がない場合、`nando admin recover` は metadata を `/recyclebin` 以下へ復元します。remote object 自体は変更しません。移行確認には `nando admin migrate` を使えますが、現行実装は schema version 1 の検証のみです。
