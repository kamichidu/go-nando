# Garbage collection

`nando gc` removes eligible Local objects. If you set a capacity limit with `init --max-local-bytes`, Capacity GC manages that limit. A value of `0` disables capacity eviction.

```sh
nando --repository "$HOME/nando-data" gc
```

Regular garbage collection does not delete remote objects. Do not run GC to prepare a backup. Use `nando doctor` for a health check.
