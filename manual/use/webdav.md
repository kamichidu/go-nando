# WebDAV

`nando-webdav` is an HTTP adapter that connects to the daemon. Its default listen address is `127.0.0.1:8080`.

```sh
nando --repository "$HOME/nando-data" webdav --webdav-listen 127.0.0.1:8080
```

The WebDAV HTTP address is independent of the daemon control address. Under the Phase A boundary, WebDAV is unauthenticated HTTP bound to loopback. Do not expose it beyond loopback. Configure WebDAV and the daemon to select the same repository.
