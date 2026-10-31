// Package sqlite provides the SQLite-backed Repository coordinator.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	sqlitedriver "modernc.org/sqlite"
)

// Coordinator serializes operations by holding a SQLite IMMEDIATE writer
// transaction on the repository's dedicated coordination database.
type Coordinator struct {
	db     *sql.DB
	leases *sql.DB
}

type ResourceLockMode string

const (
	RLock ResourceLockMode = "R"
	WLock ResourceLockMode = "W"
)

var (
	// ErrAcquire marks a failure to acquire the coordination lock. Its wrapped
	// cause preserves context cancellation/deadline and SQLite errors.
	ErrAcquire = errors.New("coordination lock acquisition failed")
	// ErrOperation marks a callback failure. Its wrapped cause remains
	// identifiable with errors.Is/errors.As.
	ErrOperation = errors.New("coordinated operation failed")
	// ErrRelease reports that the SQLite transaction or connection could not be
	// cleanly released. If returned after the callback succeeds, the callback's
	// operation may already have completed.
	ErrRelease = errors.New("coordination lock release failed")
	// ErrLeaseExpired identifies a Put whose staging Lease is no longer active.
	ErrLeaseExpired        = errors.New("put staging lease expired or was invalidated")
	ErrResourceLockExpired = errors.New("resource lock expired or was invalidated")
)

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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS resource_locks (
		resource_id TEXT NOT NULL,
		operation_id TEXT NOT NULL,
		mode TEXT NOT NULL CHECK (mode IN ('R','W')),
		expires_at INTEGER NOT NULL,
		PRIMARY KEY (resource_id, operation_id)
	)`); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize resource lock table: %w", err), db.Close())
	}
	leasePath := filepath.Join(filepath.Dir(absPath), "put-leases.db")
	leases, err := sql.Open("sqlite", leasePath+"?_txlock=immediate&_pragma=busy_timeout%3d0")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open Put Lease database %q: %w", leasePath, err), db.Close())
	}
	leases.SetMaxOpenConns(1)
	if _, err := leases.Exec(`CREATE TABLE IF NOT EXISTS put_leases (
		operation_id TEXT PRIMARY KEY,
		expires_at INTEGER NOT NULL
	)`); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize Put Lease database: %w", err), leases.Close(), db.Close())
	}
	return &Coordinator{db: db, leases: leases}, nil
}

// Close closes the database connection pool.
func (c *Coordinator) Close() error {
	return errors.Join(c.db.Close(), c.leases.Close())
}

// AcquireResourceLock atomically acquires a shared RLock or exclusive WLock
// for one resource. Conflicting live locks are waited on with context support.
func (c *Coordinator) AcquireResourceLock(ctx context.Context, resourceID, operationID string, mode ResourceLockMode, ttl time.Duration) error {
	if resourceID == "" || operationID == "" || ttl <= 0 || (mode != RLock && mode != WLock) {
		return errors.New("resource lock requires resource ID, operation ID, valid mode, and positive TTL")
	}
	for {
		acquired := false
		err := c.withCoordinationTransaction(ctx, func(conn *sql.Conn) error {
			var ownMode string
			var ownExpiry int64
			ownErr := conn.QueryRowContext(ctx, "SELECT mode,expires_at FROM resource_locks WHERE resource_id=? AND operation_id=?", resourceID, operationID).Scan(&ownMode, &ownExpiry)
			if ownErr == nil {
				var now int64
				if err := conn.QueryRowContext(ctx, "SELECT CAST(unixepoch('subsec') * 1000 AS INTEGER)").Scan(&now); err != nil {
					return err
				}
				if ownExpiry <= now {
					return ErrResourceLockExpired
				}
				if ownMode != string(mode) {
					return errors.New("resource lock mode cannot change while held")
				}
			} else if !errors.Is(ownErr, sql.ErrNoRows) {
				return ownErr
			}
			var conflicts int
			query := `SELECT count(*) FROM resource_locks WHERE resource_id=? AND expires_at > CAST(unixepoch('subsec') * 1000 AS INTEGER) AND operation_id<>? AND (mode='W' OR ?='W')`
			if err := conn.QueryRowContext(ctx, query, resourceID, operationID, string(mode)).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts != 0 {
				return nil
			}
			_, err := conn.ExecContext(ctx, `INSERT INTO resource_locks(resource_id,operation_id,mode,expires_at) VALUES(?,?,?,CAST(unixepoch('subsec') * 1000 AS INTEGER)+?) ON CONFLICT(resource_id,operation_id) DO UPDATE SET expires_at=excluded.expires_at`, resourceID, operationID, string(mode), ttlMilliseconds(ttl))
			if err == nil {
				acquired = true
			}
			return err
		})
		if err != nil {
			return err
		}
		if acquired {
			return nil
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

// RenewResourceLocks renews every still-live lock held by operationID using
// SQLite time. It never resurrects an expired lock and fails as a unit.
func (c *Coordinator) RenewResourceLocks(ctx context.Context, operationID string, ttl time.Duration) (bool, error) {
	if operationID == "" || ttl <= 0 {
		return false, errors.New("resource lock renewal requires operation ID and positive TTL")
	}
	active := false
	err := c.withCoordinationTransaction(ctx, func(conn *sql.Conn) error {
		var total, expired int
		if err := conn.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(CASE WHEN expires_at <= CAST(unixepoch('subsec') * 1000 AS INTEGER) THEN 1 ELSE 0 END),0) FROM resource_locks WHERE operation_id=?`, operationID).Scan(&total, &expired); err != nil {
			return err
		}
		if total == 0 || expired != 0 {
			return nil
		}
		_, err := conn.ExecContext(ctx, `UPDATE resource_locks SET expires_at=CAST(unixepoch('subsec') * 1000 AS INTEGER)+? WHERE operation_id=?`, ttlMilliseconds(ttl), operationID)
		active = err == nil
		return err
	})
	return active, err
}

// ValidateResourceLocks reports whether every lock held by operationID remains live.
func (c *Coordinator) ValidateResourceLocks(ctx context.Context, operationID string) (bool, error) {
	var valid bool
	err := c.withCoordinationTransaction(ctx, func(conn *sql.Conn) error {
		var total, expired int
		if err := conn.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(CASE WHEN expires_at <= CAST(unixepoch('subsec') * 1000 AS INTEGER) THEN 1 ELSE 0 END),0) FROM resource_locks WHERE operation_id=?`, operationID).Scan(&total, &expired); err != nil {
			return err
		}
		valid = total > 0 && expired == 0
		return nil
	})
	return valid, err
}

// ReleaseResourceLocks releases every resource held by operationID.
func (c *Coordinator) ReleaseResourceLocks(ctx context.Context, operationID string) error {
	if operationID == "" {
		return errors.New("resource lock release requires operation ID")
	}
	return c.withCoordinationTransaction(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM resource_locks WHERE operation_id=?", operationID)
		return err
	})
}

func (c *Coordinator) withCoordinationTransaction(ctx context.Context, operation func(*sql.Conn) error) (retErr error) {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return err
	}
	open := false
	defer func() {
		panicValue := recover()
		var releaseErr error
		if open {
			releaseErr = rollbackAndDiscardOnFailure(conn)
		}
		if closeErr := conn.Close(); closeErr != nil {
			releaseErr = errors.Join(releaseErr, closeErr)
		}
		if panicValue != nil {
			if releaseErr != nil {
				panic(errors.Join(fmt.Errorf("resource lock transaction panic: %v", panicValue), releaseErr))
			}
			panic(panicValue)
		}
		retErr = errors.Join(retErr, releaseErr)
	}()
	if err := beginImmediate(ctx, conn); err != nil {
		return err
	}
	open = true
	if err := operation(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	open = false
	return nil
}

// Exclusive holds a cross-process SQLite writer lock for the full duration of
// operation. The transaction is always rolled back because the coordination
// database stores no operation state. Context cancellation interrupts
// acquisition and is also visible to the operation. A panic rolls back before
// propagating. If rollback fails, the connection is discarded so SQLite closes
// it and releases its OS lock; release errors are returned with ErrRelease or
// attached to a re-thrown panic.
func (c *Coordinator) Exclusive(ctx context.Context, operation func() error) (err error) {
	return c.exclusive(ctx, "", false, operation)
}

// ExclusiveWithLease validates a Put Lease after acquiring Repository
// exclusion and before allowing any Commit-side mutation.
func (c *Coordinator) ExclusiveWithLease(ctx context.Context, operationID string, operation func() error) error {
	if operationID == "" {
		return errors.New("put lease operation ID is empty")
	}
	return c.exclusive(ctx, operationID, true, operation)
}

func (c *Coordinator) exclusive(ctx context.Context, operationID string, validateLease bool, operation func() error) (err error) {
	if operation == nil {
		return errors.New("coordination operation is nil")
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: acquire coordination database connection: %w", ErrAcquire, err)
	}
	transactionOpen := false
	defer func() {
		panicValue := recover()
		var releaseErr error
		if transactionOpen {
			if rollbackErr := rollbackAndDiscardOnFailure(conn); rollbackErr != nil {
				releaseErr = errors.Join(releaseErr, fmt.Errorf("%w: %w", ErrRelease, rollbackErr))
			}
		}
		if closeErr := conn.Close(); closeErr != nil {
			releaseErr = errors.Join(releaseErr, fmt.Errorf("%w: close coordination database connection: %w", ErrRelease, closeErr))
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
		return fmt.Errorf("%w: begin exclusive coordination transaction: %w", ErrAcquire, err)
	}
	transactionOpen = true
	if validateLease {
		var valid int
		if err := c.leases.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM put_leases
			WHERE operation_id = ? AND expires_at > CAST(unixepoch('subsec') * 1000 AS INTEGER)
		)`, operationID).Scan(&valid); err != nil {
			return fmt.Errorf("validate Put Lease: %w", err)
		}
		if valid == 0 {
			return fmt.Errorf("%w: %s", ErrLeaseExpired, operationID)
		}
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: canceled before callback: %w", ErrOperation, err)
	}
	if err := operation(); err != nil {
		return fmt.Errorf("%w: %w", ErrOperation, err)
	}
	return nil
}

// AcquireLease creates one operation-scoped Lease. The database clock defines
// expiry so processes on the same host use one time source.
func (c *Coordinator) AcquireLease(ctx context.Context, operationID string, ttl time.Duration) error {
	if operationID == "" || ttl <= 0 {
		return errors.New("put lease requires a non-empty operation ID and positive TTL")
	}
	return c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO put_leases (operation_id, expires_at)
			VALUES (?, CAST(unixepoch('subsec') * 1000 AS INTEGER) + ?)`, operationID, ttlMilliseconds(ttl))
		return err
	})
}

// RenewLease extends only a still-unexpired Lease belonging to operationID.
// A Lease that expired or was invalidated cannot be resurrected.
func (c *Coordinator) RenewLease(ctx context.Context, operationID string, ttl time.Duration) (bool, error) {
	if operationID == "" || ttl <= 0 {
		return false, errors.New("put lease requires a non-empty operation ID and positive TTL")
	}
	active := false
	err := c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE put_leases
			SET expires_at = CAST(unixepoch('subsec') * 1000 AS INTEGER) + ?
			WHERE operation_id = ? AND expires_at > CAST(unixepoch('subsec') * 1000 AS INTEGER)`,
			ttlMilliseconds(ttl), operationID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		active = rows == 1
		return err
	})
	return active, err
}

// InvalidateLease prevents a canceled or failed Stage from renewing or
// committing. Invalidation is ordered against Commit by Repository exclusion.
func (c *Coordinator) InvalidateLease(ctx context.Context, operationID string) error {
	if operationID == "" {
		return errors.New("put lease operation ID is empty")
	}
	return c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE put_leases SET expires_at = 0 WHERE operation_id = ?", operationID)
		return err
	})
}

// InvalidateExpiredLeases serializes Lease invalidation with Commit and keeps
// invalidated rows until their backend staging has been collected.
func (c *Coordinator) InvalidateExpiredLeases(ctx context.Context) ([]string, error) {
	var operationIDs []string
	err := c.Exclusive(ctx, func() error {
		return c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
			rows, err := conn.QueryContext(ctx, `UPDATE put_leases SET expires_at = 0
				WHERE expires_at = 0 OR expires_at <= CAST(unixepoch('subsec') * 1000 AS INTEGER)
				RETURNING operation_id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var operationID string
				if err := rows.Scan(&operationID); err != nil {
					return err
				}
				operationIDs = append(operationIDs, operationID)
			}
			return rows.Err()
		})
	})
	sort.Strings(operationIDs)
	return operationIDs, err
}

// ActiveLeaseIDs returns operation IDs whose Lease is currently unexpired.
func (c *Coordinator) ActiveLeaseIDs(ctx context.Context) ([]string, error) {
	var operationIDs []string
	err := c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT operation_id FROM put_leases
			WHERE expires_at > CAST(unixepoch('subsec') * 1000 AS INTEGER)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var operationID string
			if err := rows.Scan(&operationID); err != nil {
				return err
			}
			operationIDs = append(operationIDs, operationID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(operationIDs)
	return operationIDs, nil
}

// ForgetExpiredLease removes only an already invalidated Lease, after its
// backend staging area has been removed successfully.
func (c *Coordinator) ForgetExpiredLease(ctx context.Context, operationID string) error {
	if operationID == "" {
		return errors.New("put lease operation ID is empty")
	}
	return c.withLeaseTransaction(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM put_leases WHERE operation_id = ? AND expires_at = 0", operationID)
		return err
	})
}

func ttlMilliseconds(ttl time.Duration) int64 {
	return (ttl.Milliseconds() + 1)
}

func (c *Coordinator) withLeaseTransaction(ctx context.Context, operation func(*sql.Conn) error) (retErr error) {
	conn, err := c.leases.Conn(ctx)
	if err != nil {
		return err
	}
	transactionOpen := false
	defer func() {
		panicValue := recover()
		var releaseErr error
		if transactionOpen {
			if rollbackErr := rollbackAndDiscardOnFailure(conn); rollbackErr != nil {
				releaseErr = errors.Join(releaseErr, fmt.Errorf("rollback Put Lease transaction: %w", rollbackErr))
			}
		}
		if closeErr := conn.Close(); closeErr != nil {
			releaseErr = errors.Join(releaseErr, closeErr)
		}
		if panicValue != nil {
			if releaseErr != nil {
				panic(errors.Join(fmt.Errorf("put lease operation panicked: %v", panicValue), releaseErr))
			}
			panic(panicValue)
		}
		retErr = errors.Join(retErr, releaseErr)
	}()
	if err := beginImmediate(ctx, conn); err != nil {
		return err
	}
	transactionOpen = true
	if err := operation(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit Put Lease transaction: %w", err)
	}
	transactionOpen = false
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
		return errors.Join(fmt.Errorf("rollback SQLite transaction: %w", err), discardErr)
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
