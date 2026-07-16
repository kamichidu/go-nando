package sqlite

// MigrationV1 contains the initial schema definition for the entries table.
const MigrationV1 = `
CREATE TABLE IF NOT EXISTS entries (
    path TEXT PRIMARY KEY,
    hash TEXT NOT NULL,
    size INTEGER NOT NULL,
    mtime TEXT NOT NULL,
    mode INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    state TEXT NOT NULL,
    is_dir INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`
