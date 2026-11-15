# CLI reference

Put global options before a subcommand. Run `nando --help` or a command's `--help` option to see the options implemented by that executable.

| Command | Purpose |
| --- | --- |
| `init` | Initialize a repository |
| `daemon` | Start the daemon |
| `ls`, `stat` | Inspect the namespace |
| `put`, `get`, `cat` | Import, retrieve, or write a file to standard output |
| `mv`, `rm` | Move or remove an Entry |
| `doctor`, `gc` | Check repository health or collect eligible Local objects |
| `sync` | Present, but currently returns not implemented |
| `recovery list|retry|discard` | Inspect or handle unfinished write sessions |
| `admin recover` | Restore metadata for remote objects |
| `admin migrate` | Validate the current schema |
| `maintenance --interval DURATION` | Run a periodic maintenance client |
| `fuse`, `webdav` | Start the separate adapter executable |

Select remote storage with `init --delivery-mode remote --remote-backend gcs|s3 --remote-uri URI`. Set the Local capacity limit with `--max-local-bytes`. Use command help for the complete implemented option set.
