package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// fail switches on the error class and nothing else.
func TestFailMapsByClass(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	secret := errors.New(`open C:\Users\me\secret.db: access denied`)
	cases := []struct {
		name   string
		err    error
		status int
		code   ErrorCode
		msg    string
	}{
		{"not found", apperr.New(apperr.NotFound, "That repo isn't tracked.", secret), 404, ErrNotFound, "That repo isn't tracked."},
		{"conflict", apperr.New(apperr.Conflict, "That name is taken.", secret), 409, ErrConflict, "That name is taken."},
		{"invalid", apperr.New(apperr.Invalid, "The path isn't a folder.", secret), 400, ErrBadRequest, "The path isn't a folder."},
		{"unavailable", apperr.New(apperr.Unavailable, "Git isn't installed.", secret), 503, ErrUnavailable, "Git isn't installed."},
		{"wrapped class", fmt.Errorf("outer: %w", apperr.New(apperr.NotFound, "Gone.", secret)), 404, ErrNotFound, "Gone."},
		{"no class", secret, 500, ErrInternal, ""},
		// The API must not know the store's sentinels: a package that forgot
		// to translate gets a 500, not a guess.
		{"db sentinel without a class", fmt.Errorf("get task: %w", db.ErrNotFound), 500, ErrInternal, ""},
		{"unknown class", apperr.New(apperr.Class(99), "Odd.", secret), 500, ErrInternal, ""},
		{"empty message", apperr.New(apperr.NotFound, "", secret), 500, ErrInternal, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.fail(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil), c.err)
		if rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.status)
		}
		var body ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body %q: %v", c.name, rec.Body.String(), err)
		}
		if body.Code != c.code {
			t.Errorf("%s: code = %q, want %q", c.name, body.Code, c.code)
		}
		if c.msg != "" && body.Message != c.msg {
			t.Errorf("%s: message = %q, want %q", c.name, body.Message, c.msg)
		}
		if body.Message == "" || body.Hint == "" {
			t.Errorf("%s: message or hint is empty: %+v", c.name, body)
		}
		for _, leak := range []string{"secret", `C:\`, "access denied", "get task"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%s: body leaks %q: %s", c.name, leak, rec.Body.String())
			}
		}
	}
}

func TestTaskErrTranslatesStoreErrors(t *testing.T) {
	for _, c := range []struct {
		err   error
		class apperr.Class
	}{
		{fmt.Errorf("get task: %w", db.ErrNotFound), apperr.NotFound},
		{fmt.Errorf("create task: %w", db.ErrSubTaskDepth), apperr.Conflict},
		{fmt.Errorf("update task: %w", db.ErrConflict), apperr.Conflict},
	} {
		got, ok := apperr.ClassOf(taskErr(c.err))
		if !ok || got != c.class {
			t.Errorf("taskErr(%v) class = %v, %v; want %v", c.err, got, ok, c.class)
		}
	}
	other := errors.New("disk full")
	if _, ok := apperr.ClassOf(taskErr(other)); ok {
		t.Error("an unknown error got a class")
	}
}
