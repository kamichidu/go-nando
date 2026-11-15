# Daemon operations

One daemon process opens and owns a repository, handles CLI, FUSE, and WebDAV requests, and runs maintenance and remote delivery scheduling. Do not start multiple daemon instances against the same repository.

On Debian and Ubuntu Linux, use the [systemd system service](../setup/systemd) for ordinary operation. systemd starts the daemon, restarts it after failure, and sends SIGTERM during shutdown. It does not start FUSE or WebDAV adapters. Those are separate processes started when needed.

For a quick local check or a non-systemd environment, run the daemon in the foreground as the same operating-system account as its clients:

```sh
nando daemon
```

It uses the same config and repository resolution as the CLI. Stop it with SIGTERM for an orderly shutdown. Unfinished write sessions become Recovery items. Keep the daemon running while using the repository.
