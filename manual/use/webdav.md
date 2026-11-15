# WebDAV

`nando-webdav` は daemon に接続する HTTP adapter です。既定 listen address は `127.0.0.1:8080` です。

```sh
nando --repository "$HOME/nando-data" webdav --webdav-listen 127.0.0.1:8080
```

WebDAV HTTP address は daemon control address と独立しています。Phase A の WebDAV は loopback-only、unauthenticated HTTP です。loopback の外へ公開しないでください。daemon と WebDAV client は同じ user config または repository を選択する同等の設定を使います。
