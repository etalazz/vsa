package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// Minimal fake SQL driver to exercise PostgresPersister transaction and Exec paths.

type fakeDB struct {
	execs         []string
	failBegin     error
	failCommit    error
	failExecAt    map[int]error // 1-based index of exec call -> error
	commitCount   int
	rollbackCount int
}

type fakeDriver struct{}

type fakeConn struct{ db *fakeDB }

type fakeTx struct {
	db     *fakeDB
	closed bool
}

type fakeResult int

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

func (fakeDriver) Open(name string) (driver.Conn, error) { return &fakeConn{db: testFakeDB}, nil }

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.db.failBegin != nil {
		return nil, c.db.failBegin
	}
	return &fakeTx{db: c.db}, nil
}
func (c *fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	// Record queries
	c.db.execs = append(c.db.execs, query)
	idx := len(c.db.execs)
	if c.db.failExecAt != nil {
		if err, ok := c.db.failExecAt[idx]; ok {
			return nil, err
		}
	}
	return fakeResult(1), nil
}

func (t *fakeTx) Commit() error {
	if t.closed {
		return errors.New("already closed")
	}
	t.db.commitCount++
	t.closed = true
	if t.db.failCommit != nil {
		return t.db.failCommit
	}
	return nil
}
func (t *fakeTx) Rollback() error {
	if t.closed {
		return nil
	}
	t.db.rollbackCount++
	t.closed = true
	return nil
}

var testFakeDB *fakeDB

func init() {
	sql.Register("fakesql", fakeDriver{})
}

func newSQLDBWithFake(db *fakeDB) *sql.DB {
	testFakeDB = db
	d, _ := sql.Open("fakesql", "")
	return d
}

func TestPostgresPersister_Empty(t *testing.T) {
	db := newSQLDBWithFake(&fakeDB{})
	p := NewPostgresPersister(db, false)
	if err := p.CommitBatch(context.Background(), nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestPostgresPersister_MissingCommitID_RollsBack(t *testing.T) {
	f := &fakeDB{}
	db := newSQLDBWithFake(f)
	p := NewPostgresPersister(db, false)
	err := p.CommitBatch(context.Background(), []CommitEntry{{Key: "a"}})
	if err == nil || err.Error() != "CommitEntry.CommitID must be set" {
		t.Fatalf("unexpected err: %v", err)
	}
	if f.rollbackCount != 1 {
		t.Fatalf("expected rollback=1, got %d", f.rollbackCount)
	}
	if f.commitCount != 0 {
		t.Fatalf("expected commit=0")
	}
	if len(f.execs) != 0 {
		t.Fatalf("no execs expected, got %d", len(f.execs))
	}
}

func TestPostgresPersister_CreateMissingKeys_AndApply(t *testing.T) {
	f := &fakeDB{}
	db := newSQLDBWithFake(f)
	p := NewPostgresPersister(db, true)
	entries := []CommitEntry{{Key: "k1", Vector: 5, CommitID: "c1"}, {Key: "k2", Vector: -2, CommitID: "c2"}}
	if err := p.CommitBatch(context.Background(), entries); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if f.commitCount != 1 || f.rollbackCount != 0 {
		t.Fatalf("commit/rollback mismatch: %d/%d", f.commitCount, f.rollbackCount)
	}
	if len(f.execs) < 3 {
		t.Fatalf("expected multiple execs, got %d", len(f.execs))
	}
	// First two should be counter inserts due to createMissingKeys
	if !strings.Contains(f.execs[0], "INSERT INTO counters") || !strings.Contains(f.execs[1], "INSERT INTO counters") {
		t.Fatalf("expected initial counter inserts, got: %v", f.execs[:2])
	}
	// There should be applied_commits inserts and counters updates
	var hasApplied, hasUpdate bool
	for _, q := range f.execs {
		if strings.Contains(q, "INSERT INTO applied_commits") {
			hasApplied = true
		}
		if strings.Contains(q, "UPDATE counters SET scalar = scalar - ") {
			hasUpdate = true
		}
	}
	if !hasApplied || !hasUpdate {
		t.Fatalf("expected both applied_commits and counters update queries: %v", f.execs)
	}
}

func TestPostgresPersister_FencingToken_Update(t *testing.T) {
	f := &fakeDB{}
	db := newSQLDBWithFake(f)
	p := NewPostgresPersister(db, false)
	ft := int64(99)
	if err := p.CommitBatch(context.Background(), []CommitEntry{{Key: "k", Vector: 1, CommitID: "c", FencingToken: &ft}}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	found := false
	for _, q := range f.execs {
		if strings.Contains(q, "UPDATE counters SET last_token") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected last_token update, got: %v", f.execs)
	}
}

func TestPostgresPersister_ExecError_Rollback(t *testing.T) {
	f := &fakeDB{failExecAt: map[int]error{1: errors.New("boom")}}
	db := newSQLDBWithFake(f)
	p := NewPostgresPersister(db, true)
	err := p.CommitBatch(context.Background(), []CommitEntry{{Key: "k", Vector: 1, CommitID: "c"}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected err: %v", err)
	}
	if f.rollbackCount != 1 || f.commitCount != 0 {
		t.Fatalf("expected rollback only, got c=%d r=%d", f.commitCount, f.rollbackCount)
	}
}

func TestPostgresPersister_CommitError(t *testing.T) {
	f := &fakeDB{failCommit: errors.New("commit-fail")}
	db := newSQLDBWithFake(f)
	p := NewPostgresPersister(db, false)
	err := p.CommitBatch(context.Background(), []CommitEntry{{Key: "k", Vector: 1, CommitID: "c"}})
	if err == nil || err.Error() != "commit-fail" {
		t.Fatalf("unexpected err: %v", err)
	}
	if f.commitCount != 1 {
		t.Fatalf("expected one commit attempt")
	}
}
