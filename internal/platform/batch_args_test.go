package platform

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// One case per refused character, on any OS: presets stay portable.
func TestBatchArgsOKRejectsEachCharacter(t *testing.T) {
	for _, c := range []string{"\"", "%", "\r", "\n", "\x00", "&", "|", "<", ">", "^", "!"} {
		argv := []string{"npm", "fine", "bad" + c + "arg"}
		err := BatchArgsOK(argv)
		if !errors.Is(err, ErrBatchArg) {
			t.Errorf("%q: BatchArgsOK = %v, want ErrBatchArg", c, err)
			continue
		}
		if !strings.Contains(err.Error(), "argument 2") {
			t.Errorf("%q: error does not name the argument index: %v", c, err)
		}
		if !strings.Contains(err.Error(), "Split the command") {
			t.Errorf("%q: error does not say what to do: %v", c, err)
		}
		if strings.Contains(err.Error(), "bad") {
			t.Errorf("%q: error echoes the argument: %v", c, err)
		}
	}
	// The index is the position in argv, including argv[0].
	if err := BatchArgsOK([]string{"a&b"}); err == nil || !strings.Contains(err.Error(), "argument 0") {
		t.Errorf("argv[0]: %v", err)
	}
}

func TestBatchArgsOKAcceptsOrdinaryArguments(t *testing.T) {
	ok := []string{"mvnw.cmd", "-q", "--settings", `C:\Program Files (x86)\x y\z.json`, "a=b", "--flag=1,2;3", "'q'", "", "ünï"}
	if err := BatchArgsOK(ok); err != nil {
		t.Errorf("BatchArgsOK(%q) = %v", ok, err)
	}
	if err := BatchArgsOK(nil); err != nil {
		t.Errorf("BatchArgsOK(nil) = %v", err)
	}
}

func TestBatchArgsOKMessageNamesCharacter(t *testing.T) {
	for c, want := range map[string]string{"&": "'&'", "%": "percent sign", "\n": "line break", `"`: "double quote"} {
		err := BatchArgsOK([]string{"x", fmt.Sprintf("a%sb", c)})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want it to mention %s", c, err, want)
		}
	}
}
