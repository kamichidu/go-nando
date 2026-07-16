package metadata

import "context"

// MetadataStore defines the interface for managing path metadata inside a SQLite database.
type MetadataStore interface {
	Initialize(ctx context.Context) error
	GetEntry(ctx context.Context, path string) (Entry, error)
	ListEntries(ctx context.Context, path string) ([]Entry, error)
	PutEntry(ctx context.Context, entry Entry) error
	MoveEntry(ctx context.Context, src, dst string) error
	DeleteEntry(ctx context.Context, path string) error
	ReferencedHashes(ctx context.Context) ([]string, error)
}
