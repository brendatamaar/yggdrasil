package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassOfFindsTheClassThroughWrapping(t *testing.T) {
	cause := errors.New("sqlite: boom")
	err := fmt.Errorf("load task: %w", New(NotFound, "That task doesn't exist.", cause))
	c, ok := ClassOf(err)
	if !ok || c != NotFound {
		t.Fatalf("ClassOf = %v, %v", c, ok)
	}
	if m, ok := MessageOf(err); !ok || m != "That task doesn't exist." {
		t.Fatalf("MessageOf = %q, %v", m, ok)
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap must reach the cause")
	}
}

func TestClassOfPlainErrorHasNoClass(t *testing.T) {
	if c, ok := ClassOf(errors.New("plain")); ok {
		t.Errorf("plain error has class %v", c)
	}
	if _, ok := ClassOf(nil); ok {
		t.Error("nil has a class")
	}
}

func TestErrorTextNamesMessageAndCause(t *testing.T) {
	if got := New(Conflict, "Taken.", nil).Error(); got != "Taken." {
		t.Errorf("Error() = %q", got)
	}
	if got := New(Conflict, "Taken.", errors.New("x")).Error(); got != "Taken.: x" {
		t.Errorf("Error() = %q", got)
	}
}

func TestClassNames(t *testing.T) {
	for c, want := range map[Class]string{NotFound: "not found", Conflict: "conflict", Invalid: "invalid", Unavailable: "unavailable", 0: "unknown"} {
		if c.String() != want {
			t.Errorf("%d = %q, want %q", int(c), c.String(), want)
		}
	}
}
