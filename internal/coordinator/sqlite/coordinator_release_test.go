package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	sqlitedriver "modernc.org/sqlite"
)

func TestRollbackFailureDiscardsDatabaseSQLConnection(t *testing.T) {
	assertConnectionDiscardedAfterRollbackFailure(t, nil)
}

func TestRollbackFailurePreservesOperationAndReleaseErrors(t *testing.T) {
	operationErr := errors.New("injected operation failure")
	assertConnectionDiscardedAfterRollbackFailure(t, operationErr)
}

func assertConnectionDiscardedAfterRollbackFailure(t *testing.T, operationErr error) {
	t.Helper()
	state := &rollbackFaultState{}
	driverName := fmt.Sprintf("coordinator_rollback_fault_%d", rollbackFaultDriverID.Add(1))
	sql.Register(driverName, &rollbackFaultDriver{state: state})
	db, err := sql.Open(driverName, t.TempDir()+"/coordination.db")
	if err != nil {
		t.Fatalf("open fault-injecting SQLite database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fault-injecting database: %v", err)
		}
	})
	coordinator := &Coordinator{db: db}

	operationRan := false
	err = coordinator.Exclusive(context.Background(), func() error {
		operationRan = true
		return operationErr
	})
	if !operationRan {
		t.Fatal("callback did not run")
	}
	if !errors.Is(err, ErrRelease) || !errors.Is(err, errInjectedRollback) {
		t.Fatalf("expected identifiable rollback release failure, got %v", err)
	}
	if operationErr != nil && (!errors.Is(err, ErrOperation) || !errors.Is(err, operationErr)) {
		t.Fatalf("expected identifiable operation and callback errors as well as release errors, got %v", err)
	}
	if operationErr == nil && errors.Is(err, ErrOperation) {
		t.Fatalf("successful callback unexpectedly returned operation error: %v", err)
	}
	if closed := state.closed.Load(); closed != 1 {
		t.Fatalf("database/sql did not close the ErrBadConn connection; closed=%d", closed)
	}

	if err := coordinator.Exclusive(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("subsequent operation failed after discarding broken connection: %v", err)
	}
	if opened := state.opened.Load(); opened < 2 {
		t.Fatalf("subsequent operation did not use a replacement SQLite connection; opened=%d", opened)
	}
}

var (
	rollbackFaultDriverID atomic.Uint64
	errInjectedRollback   = errors.New("injected SQLite rollback failure")
)

type rollbackFaultState struct {
	failed atomic.Bool
	opened atomic.Int32
	closed atomic.Int32
}

type rollbackFaultDriver struct {
	state *rollbackFaultState
}

func (d *rollbackFaultDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlitedriver.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	d.state.opened.Add(1)
	return &rollbackFaultConn{Conn: conn, state: d.state}, nil
}

type rollbackFaultConn struct {
	driver.Conn
	state *rollbackFaultState
}

func (c *rollbackFaultConn) Close() error {
	c.state.closed.Add(1)
	return c.Conn.Close()
}

func (c *rollbackFaultConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.EqualFold(strings.TrimSpace(query), "ROLLBACK") && c.state.failed.CompareAndSwap(false, true) {
		return nil, errInjectedRollback
	}
	execer, ok := c.Conn.(interface {
		ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error)
	})
	if !ok {
		return nil, errors.New("modernc SQLite connection does not implement ExecContext")
	}
	return execer.ExecContext(context.Background(), query, nil)
}
