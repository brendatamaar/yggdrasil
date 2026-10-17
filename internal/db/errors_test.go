package db

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// One test per mapping of a SQLite extended result code to a sentinel.

func TestUniqueViolationIsConflict(t *testing.T) {
	s := NewStore(openTest(t))
	ctx := context.Background()
	if _, err := s.CreateProject(ctx, core.Project{Name: "acme-shop", Path: "/work/a"}); err != nil {
		t.Fatal(err)
	}
	// projects.name is UNIQUE (SQLITE_CONSTRAINT_UNIQUE, 2067).
	_, err := s.CreateProject(ctx, core.Project{Name: "acme-shop", Path: "/work/b"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name = %v, want ErrConflict", err)
	}
	if code, ok := sqliteCode(err); !ok || code != codeConstraintUnique {
		t.Errorf("code = %d, %v; the test no longer exercises a UNIQUE violation", code, ok)
	}
}

func TestPrimaryKeyViolationIsConflict(t *testing.T) {
	s := NewStore(openTest(t))
	ctx := context.Background()
	if _, err := s.CreateProject(ctx, core.Project{ID: "p1", Name: "one", Path: "/work/a"}); err != nil {
		t.Fatal(err)
	}
	// The same ID twice (SQLITE_CONSTRAINT_PRIMARYKEY, 1555).
	_, err := s.CreateProject(ctx, core.Project{ID: "p1", Name: "two", Path: "/work/b"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate id = %v, want ErrConflict", err)
	}
	if code, ok := sqliteCode(err); !ok || code != codeConstraintPrimaryKey {
		t.Errorf("code = %d, %v; the test no longer exercises a PRIMARY KEY violation", code, ok)
	}
}

func TestForeignKeyViolationIsNotFound(t *testing.T) {
	s := NewStore(openTest(t))
	ctx := context.Background()
	// A repo of a project that does not exist (SQLITE_CONSTRAINT_FOREIGNKEY, 787).
	_, err := s.CreateRepo(ctx, core.Repo{ProjectID: "missing", Name: "shop-api", Path: "/work/shop-api"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("repo of a missing project = %v, want ErrNotFound", err)
	}
	if code, ok := sqliteCode(err); !ok || code != codeConstraintForeignKey {
		t.Errorf("code = %d, %v; the test no longer exercises a FOREIGN KEY violation", code, ok)
	}
}

func TestSubTaskTriggerIsErrSubTaskDepth(t *testing.T) {
	s := NewStore(openTest(t))
	ctx := context.Background()
	p, r := seed(t, s)
	parent, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTask(ctx, newTask(p, r, &parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateTask(ctx, newTask(p, r, &child.ID))
	if !errors.Is(err, ErrSubTaskDepth) {
		t.Fatalf("grandchild = %v, want ErrSubTaskDepth", err)
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		t.Errorf("trigger abort also matches another sentinel: %v", err)
	}
}

// A trigger abort that is not the sub-task rule (the append-only audit log)
// must not be reported as one.
func TestOtherTriggerAbortIsNotMapped(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s := NewStore(d)
	e := core.AuditEntry{ID: "aud_1", Actor: "system", Action: "x", Subject: "task:1"}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.AppendAudit(ctx, e) }); err != nil {
		t.Fatal(err)
	}
	_, err := d.Write.ExecContext(ctx, "UPDATE audit_log SET actor = 'y'")
	if err == nil {
		t.Fatal("audit log was updated")
	}
	got := classify(err)
	if errors.Is(got, ErrSubTaskDepth) || errors.Is(got, ErrConflict) || errors.Is(got, ErrNotFound) {
		t.Errorf("classify(%v) = %v; the audit trigger must stay unmapped", err, got)
	}
}

func TestClassifyLeavesOtherErrorsAlone(t *testing.T) {
	plain := errors.New("disk full")
	if got := classify(plain); got != plain {
		t.Errorf("classify changed a plain error: %v", got)
	}
	if classify(nil) != nil {
		t.Error("classify(nil) != nil")
	}
}
