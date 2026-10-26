// Package sqlite provides the SQLite-backed Repository coordinator.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	sqlitedriver "modernc.org/sqlite"
)

// Coordinator serializes operations by holding a SQLite IMMEDIATE writer
// transaction on the repository's dedicated coordination database.
type Coordinator struct {
	db *sql.DB
}

// ErrRelease reports that the SQLite transaction or connection could not be
// cleanly released. If returned after the callback succeeds, the callback's
// operation may already have completed.
var ErrRelease = errors.New("coordination lock release failed")

// Open opens the coordination database at dbPath. The path is made absolute
// so callers using different working directories still address the same file.
func Open(dbPath string) (*Coordinator, error) {
	if dbPath == "" {
		return nil, errors.New("coordination database path is empty")
	}
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve coordination database path: %w", err)
	}
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return nil, fmt.Errorf("open coordination database %q: %w", absPath, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 0"); err != nil {
		return nil, errors.Join(fmt.Errorf("configure coordination database busy timeout: %w", err), db.Close())
	}
	return &Coordinator{db: db}, nil
}

// Close closes the database connection pool.
func (c *Coordinator) Close() error {
	return c.db.Close()
}

// Exclusive holds a cross-process SQLite writer lock for the full duration of
// operation. The transaction is always rolled back because the coordination
// database stores no operation state. Context cancellation interrupts
// acquisition and is also visible to the operation. A panic rolls back before
// propagating. If rollback fails, the connection is discarded so SQLite closes
// it and releases its OS lock; release errors are returned with ErrRelease or
// attached to a re-thrown panic.
func (c *Coordinator) Exclusive(ctx context.Context, operation func() error) (err error) {
	if operation == nil {
		return errors.New("coordination operation is nil")
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire coordination database connection: %w", err)
	}
	transactionOpen := false
	defer func() {
		panicValue := recover()
		var releaseErr error
		if transactionOpen {
			if rollbackErr := rollbackAndDiscardOnFailure(conn); rollbackErr != nil {
				releaseErr = errors.Join(releaseErr, fmt.Errorf("%w: %v", ErrRelease, rollbackErr))
			}
		}
		if closeErr := conn.Close(); closeErr != nil {
			releaseErr = errors.Join(releaseErr, fmt.Errorf("%w: close coordination database connection: %v", ErrRelease, closeErr))
		}
		if panicValue != nil {
			if releaseErr != nil {
				panic(errors.Join(fmt.Errorf("coordination operation panicked: %v", panicValue), releaseErr))
			}
			panic(panicValue)
		}
		err = errors.Join(err, releaseErr)
	}()
	if err = beginImmediate(ctx, conn); err != nil {
		return fmt.Errorf("begin exclusive coordination transaction: %w", err)
	}
	transactionOpen = true

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := operation(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func rollbackAndDiscardOnFailure(conn *sql.Conn) error {
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err == nil {
		return nil
	} else {
		discardErr := conn.Raw(func(any) error { return driver.ErrBadConn })
		if errors.Is(discardErr, driver.ErrBadConn) {
			discardErr = nil
		}
		return errors.Join(fmt.Errorf("rollback coordination transaction: %w", err), discardErr)
	}
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			return nil
		} else {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !isSQLiteBusy(err) {
				return err
			}
		}

		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 5 // SQLITE_BUSY
}
