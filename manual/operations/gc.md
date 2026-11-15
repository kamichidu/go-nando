# Garbage collection

`nando gc` removes eligible unreferenced Local objects. Capacity GC uses `init --max-local-bytes` only for Remote-mode repositories, evicting cache objects after they have been delivered and confirmed in remote storage. Local-mode content is durable in Local storage and is not evicted by Capacity GC. A value of `0` disables capacity eviction. Capacity GC runs as part of the daemon scheduler; Remote mode requires the complete scheduler policy described in [remote setup](../setup/configuration).

```sh
nando --repository "$HOME/nando-data" gc
```

Regular garbage collection does not delete remote objects. Do not run GC to prepare a backup. Use `nando doctor` for a health check.
