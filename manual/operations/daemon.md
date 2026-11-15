# Daemon operations

One daemon process opens and owns a repository, handles CLI, FUSE, and WebDAV requests, and runs maintenance. Do not start multiple daemon instances against the same repository.

For a systemd system service, follow the [systemd setup procedure](../setup/systemd). To run the daemon in the foreground:

```sh
nando --repository "$HOME/nando-data" daemon
```

Stop the foreground daemon with SIGTERM for an orderly shutdown. Unfinished write sessions become Recovery items. The remote delivery scheduler runs while the daemon is active.
