# nando

*nando* is written as 「納戸」, the Japanese word for a household storeroom or closet. The name reflects the idea of keeping familiar files in a familiar place while the storage behind that place can grow far beyond the space in a home.

## Why nando?

Imagine that your home storeroom opens onto a vast place for keeping things. You want to sort and store files with ordinary file operations, without having to think about a dedicated command line interface, an API, or remote usage charges every time you tidy up. When you need the files, you want to bring them close and work with them together. If two files have the same content, you want nando to avoid storing that content twice.

Different storage products make different tradeoffs. nando explores this particular way of working; it does not make other approaches less useful.

Today, nando stores content locally and can deliver it asynchronously to one selected GCS or S3-compatible backend. It deduplicates content by content ID. It provides a CLI, a FUSE filesystem adapter, and a loopback-only WebDAV adapter. Remote delivery is not a mounted cloud drive: files are committed locally first, delivery happens in the background, and a successful write does not confirm remote durability. See [current limitations](./reference/limitations) for the implemented boundaries.

## Start here

- [Overview and architecture](./getting-started/concepts)
- [Install nando](./getting-started/install)
- [Quick start](./getting-started/quickstart)
- [Configure a remote backend](./setup/gcs)
- [Run a systemd system service](./setup/systemd)
- [Back up and restore a repository](./operations/backup-restore)

See the [CLI reference](./reference/cli), [configuration examples](https://github.com/kamichidu/go-nando/blob/main/samples/config.toml), and [configuration reference](./reference/configuration) for more detail.
