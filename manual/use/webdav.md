# WebDAV: start, use, and stop

`nando-webdav` is a separate HTTP adapter. It connects to the daemon control endpoint and serves a WebDAV interface on a separate address. On a Debian package, invoke it through `nando webdav`.

## Start

Initialize the repository and start its daemon first. Start the adapter with the same repository selection and daemon endpoint:

```sh
nando webdav
```

The default address is `http://127.0.0.1:8080/`; set the bind address with `--webdav-listen` or `[webdav].listen`. With shared XDG defaults, no explicit repository path is needed. A system-service repository requires running the adapter as the `nando` account with that instance config; see [systemd setup](../setup/systemd).

The HTTP service is unauthenticated plain HTTP and is restricted to loopback. Do not expose it beyond the local host. The WebDAV HTTP address is independent of the daemon control address.

## Implemented HTTP methods

The following method surface is implemented by the adapter. This is an explicit subset; it does not claim full WebDAV client or protocol compliance.

| HTTP method | Current behavior |
| --- | --- |
| `GET`, `HEAD` | Read file content or response metadata. |
| `OPTIONS` | Reports DAV class 1 and advertises the supported methods. |
| `PROPFIND` | Reads resource and collection metadata, including directory listings. |
| `PUT` | Creates or replaces a file and commits the complete request body before returning success. New files return `201`; replacement returns `204`. |
| `MKCOL` | Creates a collection; a request body is unsupported. |
| `DELETE` | Removes a resource through the repository namespace operation. The root cannot be deleted. |
| `MOVE` | Moves a resource to an absent destination. Replacement is unsupported; an absolute destination with a different host is rejected. |
| `COPY` | Copies a file to an absent destination. Directory COPY, destination replacement, and an absolute destination with a different host are unsupported. |

PUT supports `If-Match` and `If-None-Match` against the Entry snapshot captured when the write begins. The implementation also handles `If-Unmodified-Since`. The `If`, `If-Modified-Since`, and `If-Range` headers are rejected for PUT. A precondition conflict does not commit the request body.

`LOCK`, `UNLOCK`, and `PROPPATCH` return `501 Not Implemented`. Locking and dead-property updates are not implemented; PROPFIND does not advertise the unsupported lock property. A WebDAV client that requires these features or replacement semantics may not work with this adapter. The project checks a targeted method subset; it does not claim full WebDAV compliance or broad client interoperability.

For a simple file transfer and collection listing using curl:

```sh
curl --upload-file ./photo.jpg http://127.0.0.1:8080/photo.jpg
curl --output ./photo-copy.jpg http://127.0.0.1:8080/photo.jpg
curl -X PROPFIND -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data '<propfind xmlns="DAV:"><allprop/></propfind>' \
  http://127.0.0.1:8080/
```

These are HTTP `PUT`, `GET`, and `PROPFIND` requests. They do not use the FUSE filesystem operation interface.

## Write and commit behavior

A successful PUT response is sent only after the request body has been written to staging, synced, and committed through the daemon. This makes the HTTP response a synchronous repository commit result for that PUT. It does not confirm asynchronous delivery to a configured remote backend.

## Stop

Press Ctrl-C or send SIGTERM to the adapter process. It performs a graceful HTTP shutdown. Stop the WebDAV adapter before stopping the daemon. systemd supervises the daemon but does not start this adapter.
