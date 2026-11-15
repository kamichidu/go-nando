# Recovery

After daemon interruption, inspect any unfinished write sessions:

```sh
nando recovery list
nando recovery retry SESSION_ID
nando recovery discard SESSION_ID
```

`retry` attempts to commit the session content again; `discard` removes its Recovery staging data. Review the session ID, repository path, target Entry, and failure reason first. Both commands require a running daemon.

If a remote object has no corresponding metadata Entry, `nando admin recover` restores metadata under `/recyclebin` without changing the remote object. `nando admin migrate` currently validates schema revision 1 only.
