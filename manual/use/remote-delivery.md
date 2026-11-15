# Asynchronous remote delivery

In Remote mode, complete content is committed locally first. The metadata transaction commits the Entry and its delivery obligation together. The daemon scheduler then delivers the content to the repository's single selected GCS or S3-compatible backend according to the idle interval, periodic interval, maximum delivery delay, and parallelism settings.

A successful CLI write does not mean the remote copy is durable. Delivery obligations remain queued while the remote service is unavailable, and the daemon retries them later. Preserve the local repository and undelivered content when stopping or moving the daemon.

Set all four scheduler values in the daemon TOML file under `[remote]`. Credentials come from the daemon account's GCS ADC or AWS SDK credential chain. There is no CLI command to report when the delivery queue has drained.
