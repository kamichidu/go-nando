# Asynchronous remote delivery

In Remote mode, content is committed locally first. The daemon scheduler delivers content to the repository's one selected GCS or S3-compatible backend, using the configured idle interval, periodic interval, maximum delivery delay, and parallelism.

A successful CLI write does not mean the remote copy is durable. If the remote service is unavailable, delivery obligations stay queued for later retry. Preserve the local repository and undelivered content when stopping or moving the daemon.

Set all four scheduler values in the daemon TOML file under `[remote]`. Credentials come from the daemon account's GCS ADC or AWS SDK credential chain. No CLI command reports when the delivery queue has drained.
