package tasks

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// TestChaosNoProcessOutlivesAFinishedTask throws random messages, cancels,
// dismissals and maxAgents changes at the service from several goroutines. When
// it settles, every invariant that matters must hold: no run is alive, every
// task that ran was killed, no task is left live, the board and the locks are
// empty, the worktrees of a task are unique per repo, and the number of live
// runs never passed maxAgents.
func TestChaosNoProcessOutlivesAFinishedTask(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			h := newHarness(t, []string{"shop-api", "shop-web", "shop-admin"})
			h.adapter.caps.Enforcement = agents.EnforcementHook
			h.adapter.during = func(agents.StartOptions) { time.Sleep(time.Duration(rand.IntN(4)) * time.Millisecond) }
			h.start()
			ctx := context.Background()

			var mu sync.Mutex
			var ids []core.TaskID
			pick := func(r *rand.Rand) (core.TaskID, bool) {
				mu.Lock()
				defer mu.Unlock()
				if len(ids) == 0 {
					return "", false
				}
				return ids[r.IntN(len(ids))], true
			}
			var wg sync.WaitGroup
			for w := 0; w < 4; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					r := rand.New(rand.NewPCG(seed, uint64(w)))
					for i := 0; i < 25; i++ {
						switch r.IntN(7) {
						case 0, 1, 2: // a new task
							mode := []string{"ask", "ask", "code", "current_branch"}[r.IntN(4)]
							res, err := h.svc.Message(ctx, h.msg(mode, "go", r.IntN(3)))
							if err == nil {
								mu.Lock()
								ids = append(ids, res.Task.ID)
								mu.Unlock()
							}
						case 3: // another turn of a task
							if id, ok := pick(r); ok {
								req := h.msg([]string{"ask", "code"}[r.IntN(2)], "again", 0)
								req.TaskID = id
								_, _ = h.svc.Message(ctx, req)
							}
						case 4, 5: // cancel
							if id, ok := pick(r); ok {
								_, _ = h.svc.Cancel(ctx, id)
							}
						case 6:
							if r.IntN(2) == 0 {
								_ = h.svc.SetMaxAgents(ctx, fmt.Sprint(1+r.IntN(3)))
							} else if id, ok := pick(r); ok {
								_, _ = h.svc.Dismiss(ctx, id)
							}
						}
						time.Sleep(time.Duration(r.IntN(3)) * time.Millisecond)
					}
				}(w)
			}
			wg.Wait()

			// Settle: nothing is live any more.
			waitFor(t, 30*time.Second, func() bool {
				for _, id := range ids {
					if isLive(h.state(id)) {
						return false
					}
				}
				return h.boardActive() == 0 && !h.svcAnyWaiting()
			}, "all tasks to settle")

			if n := h.adapter.live.Load(); n != 0 {
				t.Errorf("%d run(s) still alive after every task finished", n)
			}
			started := map[core.TaskID]bool{}
			for _, o := range h.adapter.Starts() {
				started[o.TaskID] = true
			}
			for id := range started {
				if h.killer.count(id) == 0 {
					t.Errorf("task %s ran but its processes were never killed", id)
				}
			}
			for _, id := range ids {
				st := h.state(id) // also proves every row loads back
				if st.Row.Status.Terminal() || st.Row.Status == core.TaskReview || st.Row.Status == core.TaskNeedsYou {
					wts, _ := h.store.ListWorktrees(ctx, id)
					seen := map[core.RepoID]bool{}
					for _, w := range wts {
						if seen[w.RepoID] {
							t.Errorf("task %s has two worktrees for repo %s", id, w.RepoID)
						}
						seen[w.RepoID] = true
					}
				} else {
					t.Errorf("task %s settled as %s", id, st.Row.Status)
				}
				// The last status published is the stored one.
				pub := h.hub.statusesOf(id)
				if len(pub) > 0 && pub[len(pub)-1] != st.Row.Status {
					t.Errorf("task %s: last published %s, stored %s", id, pub[len(pub)-1], st.Row.Status)
				}
			}
			h.svc.boardMu.Lock()
			locks, left := len(h.svc.board.Locks), len(h.svc.board.Tasks)
			h.svc.boardMu.Unlock()
			if locks != 0 || left != 0 {
				t.Errorf("board not empty: %d locks, %d tasks", locks, left)
			}
			for _, repo := range h.repos {
				if holder := h.lock.held(repo.ID); holder != "" {
					t.Errorf("main checkout of %s still locked by %s", repo.Name, holder)
				}
			}
			if m := h.adapter.maxLive.Load(); m > 3 {
				t.Errorf("%d runs were live at once; maxAgents never exceeded 3", m)
			}
			var _ coretasks.State
		})
	}
}

func (h *harness) svcAnyWaiting() bool { return h.svc.anyWaiting() }

func (h *harness) boardActive() int {
	h.svc.boardMu.Lock()
	defer h.svc.boardMu.Unlock()
	return h.svc.board.Active()
}
