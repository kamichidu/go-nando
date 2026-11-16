# Garbage collection

`nando gc` removes eligible unreferenced Local objects. Capacity GC applies only to Remote-mode repositories:

- It uses the limit set with `init --max-local-bytes`.
- It evicts cache objects only after remote delivery is confirmed.
- A limit of `0` disables capacity eviction.

Local-mode content is durable in Local storage and is not evicted by Capacity GC. Capacity GC runs as part of the daemon scheduler. Remote mode requires the complete scheduler policy described in [remote setup](../setup/configuration).

With the XDG defaults used by the [Quick Start](../getting-started/quickstart), run:

```sh
nando gc
```

Run GC while the daemon is active and select the same repository as the daemon. For a different repository, pass the same `--repository /path/to/repository` or the same `--config /path/to/config.toml` to the daemon, CLI, and FUSE or WebDAV adapter you use.

Regular garbage collection does not delete remote objects. Do not run GC to prepare a backup. Use `nando doctor` for a health check.
