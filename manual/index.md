# nando

*nando* is written as 「納戸」, the Japanese word for a household storeroom or closet. The name reflects the idea of keeping familiar files in a familiar place while the storage behind that place can grow far beyond the space in a home.

## Why nando?

Imagine that your home storeroom opens onto a vast place for keeping things. You want to sort and store files with ordinary file operations, without having to think about a dedicated command line interface, an API, or remote usage charges every time you tidy up. When you need the files, you want to bring them close and work with them together. If two files have the same content, you want nando to avoid storing that content twice.

Different storage products make different tradeoffs. nando explores this particular way of working; it does not make other approaches less useful.

Today, nando is designed for everyday file access through its FUSE filesystem adapter or loopback-only WebDAV adapter. The CLI is for initialization, administration, diagnostics, and debugging. nando stores content locally and can deliver it asynchronously to one selected GCS or S3-compatible backend; it deduplicates content by content ID. Remote delivery is not a mounted cloud drive: files are committed locally first, delivery happens in the background, and a successful write does not confirm remote durability. See [current limitations](./reference/limitations) for the implemented boundaries.

## Start here

1. [Install nando](./getting-started/install).
2. [Initialize a repository and follow the Quick Start](./getting-started/quickstart).
3. Start its daemon in the foreground for personal use, or follow the [systemd system-service procedure](./setup/systemd) for a packaged system repository.
4. Choose [FUSE](./use/fuse) or [WebDAV](./use/webdav) for everyday file access.

The [CLI administration guide](./use/cli) and [CLI reference](./reference/cli) cover setup, maintenance, and diagnostics. See [remote backend setup](./setup/gcs), [configuration examples](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml), and the [configuration reference](./reference/configuration) for more detail. The [backup and restore guide](./operations/backup-restore) covers repository operations.
