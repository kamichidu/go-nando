package sqlite

// MigrationV1 contains the initial schema definition for the entries table.
const MigrationV1 = `
CREATE TABLE IF NOT EXISTS entries (
    entry_id      TEXT PRIMARY KEY,
    parent_id     TEXT NULL,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL,
    object_id     TEXT NULL,
    size          INTEGER NOT NULL,
    mtime         TEXT NOT NULL,
    mode          INTEGER NOT NULL,
    content_type  TEXT NOT NULL,
    revision      INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE (parent_id, name)
);
`
