# Garbage collection

`nando gc` は Local object の不要データを回収します。`init --max-local-bytes` で capacity 上限を設定した場合、Capacity GC がこの上限を管理します。0 は capacity eviction を無効にします。

```sh
nando --repository "$HOME/nando-data" gc
```

通常の GC は remote object を削除しません。GC を backup の準備操作として実行しないでください。health check には `nando doctor` を使います。
