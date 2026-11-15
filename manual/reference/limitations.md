# Limitations

This page summarizes current boundaries that are not repeated throughout the manual.

- Compatibility with repository, configuration, or Recovery state from before `v1.0.0` is not guaranteed.
- A repository selects at most one remote backend. Replication, failover, and tiering across providers are unsupported.
- Remote delivery is asynchronous; a successful write does not confirm remote durability.
- URI prefixes are literal. A trailing `/` is not added automatically.
- S3 upload uses a single `PutObject`; multipart upload is not implemented.
- WebDAV is unauthenticated HTTP bound to loopback.
- The system service socket is restricted to its dedicated service account. Authorization for clients running as multiple operating-system accounts is not implemented.
- General SQLite schema and Repository Format migrations are not implemented. The v1 baseline is the only supported schema.
- There is no CLI command to report queue drain, no automatic binary update, and no automatic Repository Format or backend switch.
- The `nando sync` command is present but the synchronization operation is not implemented. Remote delivery is handled by the daemon scheduler.
