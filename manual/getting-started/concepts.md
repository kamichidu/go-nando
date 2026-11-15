# Core concepts

| Concept | Meaning |
| --- | --- |
| Repository | A storage unit containing settings, a metadata database, local objects, and staging data |
| Entry | A file or directory name and its metadata within a repository |
| Object | Immutable file content identified by a content ID |
| Daemon | The process that opens a repository and handles CLI, FUSE, and WebDAV requests |
| Local mode | Local object storage is the durable authority |
| Remote mode | Content commits locally, then is delivered asynchronously to one selected remote backend |

Metadata determines which Entries are visible, regardless of whether remote delivery has completed. Each repository uses either Local mode or Remote mode with one backend. Multiple remote providers are not supported for one repository.
