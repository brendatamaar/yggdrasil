package tasks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// TestEveryCoreCommandIsKnownToTheShell: the commands the core can return are
// found in its source (every type with an isCommand method); the shell must
// know each of them, so a new variant cannot be silently ignored.
func TestEveryCoreCommandIsKnownToTheShell(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../core/tasks/command.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "isCommand" {
			continue
		}
		if id, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
			declared = append(declared, id.Name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no command types in the core")
	}

	all := []coretasks.Command{
		coretasks.PersistTurn{}, coretasks.Enqueue{}, coretasks.Requeue{}, coretasks.Dequeue{}, coretasks.PrepareRun{},
		coretasks.StartRun{}, coretasks.KillRun{}, coretasks.FinishTurn{}, coretasks.ReleaseLocks{}, coretasks.KillTask{},
		coretasks.Publish{}, coretasks.Audit{}, coretasks.Reject{}, coretasks.Admit{},
	}
	var listed []string
	for _, c := range all {
		listed = append(listed, reflect.TypeOf(c).Name())
	}
	sort.Strings(declared)
	sort.Strings(listed)
	if !reflect.DeepEqual(declared, listed) {
		t.Fatalf("the core declares commands %v but this test lists %v: teach the shell (interpret.dispatch) the new ones and update the list", declared, listed)
	}

	h := newHarness(t, []string{"shop-api"})
	h.start()
	_ = h.svc.Close() // dispatch only records effects; nothing runs
	for _, c := range all {
		var fx effects
		if !h.svc.dispatch(core.TaskID("tsk_0000000000000001"), c, &fx) {
			t.Errorf("the shell does not know the command %T", c)
		}
	}
	var fx effects
	if h.svc.dispatch("tsk_0000000000000001", bogus{}, &fx) {
		t.Error("an invented command was accepted")
	}
}
