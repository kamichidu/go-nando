# Access a repository with WebDAV

`nando-webdav` is a separate HTTP adapter. It connects to the daemon control endpoint and exposes WebDAV on a second, independent address. On a Debian package, invoke it through `nando webdav`.

Initialize the repository and start its daemon first. Then start the adapter using the same config and repository selection:

```sh
nando webdav
```

The default WebDAV address is `127.0.0.1:8080`; set it with `--webdav-listen` or `[webdav].listen`. With the shared XDG defaults, no explicit repository path is needed. A system service's daemon endpoint and repository must be selected using the same instance config, and the adapter must run as an account authorized to access that service socket.

The WebDAV endpoint is unauthenticated HTTP bound to loopback. Do not expose it beyond loopback. The WebDAV HTTP address does not configure the daemon control connection. The [systemd service](../setup/systemd) does not start this adapter automatically.
