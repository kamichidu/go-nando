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

// MigrationV3 stores the locally observed MD5 for each committed content object.
const MigrationV3 = `ALTER TABLE entries ADD COLUMN object_md5 BLOB NULL;`

// MigrationV4 stores immutable integrity metadata by content identity.
const MigrationV4 = `
CREATE TABLE content_metadata (
    content_id TEXT PRIMARY KEY,
    md5 BLOB NOT NULL CHECK(length(md5) = 16)
);
INSERT INTO content_metadata(content_id, md5)
SELECT object_id, min(object_md5) FROM entries
WHERE object_id IS NOT NULL
GROUP BY object_id
HAVING count(DISTINCT hex(object_md5)) = 1 AND min(length(object_md5)) = 16;
ALTER TABLE entries DROP COLUMN object_md5;
`

const CurrentSchemaVersion = 4
