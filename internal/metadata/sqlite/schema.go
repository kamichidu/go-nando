package sqlite

// MigrationV1 creates the initial committed-entry schema and records the
// repository format metadata in the same SQLite database.
const MigrationV1 = `
CREATE TABLE entries (
    entry_id      TEXT PRIMARY KEY,
    parent_id     TEXT NULL,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    object_id     TEXT NULL,
    size          INTEGER NOT NULL,
    mtime         INTEGER NOT NULL,
    mode          INTEGER NOT NULL,
    content_type  TEXT NOT NULL,
    revision      INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE (parent_id, name)
);
CREATE TABLE repository_format (
    singleton          INTEGER PRIMARY KEY CHECK (singleton = 1),
    format_version     INTEGER NOT NULL,
    content_algorithm  TEXT NOT NULL,
    content_id_format  TEXT NOT NULL
);
CREATE TABLE schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  INTEGER NOT NULL
);
`

// MigrationV2 adds the durable content-ID transfer queue. Queue rows are
// obligations, not logical Entry state or resumable Put progress.
const MigrationV2 = `
CREATE TABLE transfer_queue (
    content_id TEXT PRIMARY KEY,
    enqueued_at INTEGER NOT NULL
);
`

const CurrentSchemaVersion = 2
