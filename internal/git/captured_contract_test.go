package git

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Unknown optional termination evidence deliberately fails closed.
type capturedProcess struct {
	code    int
	err     error
	pipeCut bool
	wait    func()
}

func (*capturedProcess) Pid() int    { return 1 }
func (*capturedProcess) Kill() error { return nil }
func (p *capturedProcess) Wait() (int, error) {
	if p.wait != nil {
		p.wait()
	}
	return p.code, p.err
}
func (p *capturedProcess) Truncated() bool { return p.pipeCut }

func TestCapturedProcessContract(t *testing.T) {
	waitErr := errors.New("fictional wait failure")
	startErr := errors.New("fictional start failure")
	for _, tc := range []struct {
		name                      string
		code                      int
		overflow, pipeCut, cancel bool
		waitErr, startErr         error
	}{
		{name: "overflow natural failure", code: 7, overflow: true},
		{name: "overflow success", overflow: true},
		{name: "complete success"},
		{name: "pipe cutoff", pipeCut: true},
		{name: "pipe cutoff failure", pipeCut: true, code: 7},
		{name: "wait precedence", code: 7, overflow: true, waitErr: waitErr},
		{name: "cancel precedence", code: 7, overflow: true, waitErr: waitErr, cancel: true},
		{name: "start failure", startErr: startErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g := &Git{exe: "fictional-git", env: []string{"PATH=fictional"}, taskID: "fictional-task", redact: safety.NewRedactor("fictional-secret")}
			g.start = func(runCtx context.Context, spec platform.Spec) (platform.Process, error) {
				if spec.Dir != "fictional-dir" || spec.TaskID != g.taskID || !slices.Equal(spec.Env, g.env) || !slices.Contains(spec.Argv, "--no-optional-locks") {
					t.Fatal("process policy lost")
				}
				if tc.startErr != nil {
					return nil, tc.startErr
				}
				p := &capturedProcess{code: tc.code, err: tc.waitErr, pipeCut: tc.pipeCut}
				p.wait = func() {
					text := "abcd"
					if tc.overflow {
						text += "ef"
					}
					_, _ = io.WriteString(spec.Stdout, text)
					_, _ = io.WriteString(spec.Stderr, "fictional-secret "+strings.Repeat("x", 100))
					if tc.overflow && runCtx.Err() == nil {
						t.Fatal("overflow did not cancel child context")
					}
					if tc.cancel {
						cancel()
					}
				}
				return p, nil
			}
			out, cut, err := g.runCaptured(ctx, "fictional-dir", "contract", true, 4, 32, "log")
			expected := tc.startErr
			if expected == nil {
				expected = tc.waitErr
			}
			if tc.cancel {
				expected = context.Canceled
			}
			if expected != nil {
				if !errors.Is(err, expected) || out != "" || cut {
					t.Fatalf("error precedence: %q %v %v", out, cut, err)
				}
			} else if tc.code != 0 {
				var ge *Error
				if !errors.As(err, &ge) || ge.Code != tc.code || out != "" || cut {
					t.Fatalf("natural failure hidden: %q %v %v", out, cut, err)
				}
				if strings.Contains(ge.Stderr, "fictional-secret") || !strings.Contains(ge.Stderr, safety.Redacted) || len(ge.Stderr) > 32 || strings.Contains(ge.Error(), "fictional-secret") {
					t.Fatalf("stderr privacy/bound: %v", ge)
				}
			} else if err != nil || out != "abcd" || cut != (tc.overflow || tc.pipeCut) {
				t.Fatalf("capture metadata: %q %v %v", out, cut, err)
			}
		})
	}
}

// The test binary produces output until cutoff cancellation kills it.
func TestCapturedOverflowHelper(t *testing.T) {
	if os.Getenv("YGG_CAPTURE_TEST") != "1" {
		return
	}
	for {
		if _, err := io.WriteString(os.Stdout, strings.Repeat("x", 8192)); err != nil {
			os.Exit(2)
		}
	}
}

func TestCapturedRealTermination(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var proc platform.Process
	g := &Git{exe: exe, env: []string{"YGG_CAPTURE_TEST=1"}, redact: safety.NewRedactor()}
	g.start = func(ctx context.Context, spec platform.Spec) (platform.Process, error) {
		spec.Argv = []string{exe, "-test.run=^TestCapturedOverflowHelper$"}
		p, err := platform.Start(ctx, spec)
		proc = p
		return p, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, cut, err := g.runCaptured(ctx, t.TempDir(), "real cutoff", true, 1024, 32)
	if err != nil || !cut || out != strings.Repeat("x", 1024) || proc == nil || !platform.WasTerminated(proc) {
		t.Fatalf("real cutoff termination: bytes=%d cut=%v err=%v", len(out), cut, err)
	}
}
