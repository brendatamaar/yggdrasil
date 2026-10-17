//go:build !windows

package platform

import "sync"

// Unix has no task jobs (docs/AGENTS.md 5.2: minimal). KillTask instead walks a
// registry of the running processes that carry a TaskID and kills each one's
// process group.
var taskReg = struct {
	mu   sync.Mutex
	next int
	m    map[string]map[int]func()
}{m: map[string]map[int]func(){}}

// registerTask records kill under taskID and returns the function that removes
// it. An empty taskID registers nothing.
func registerTask(taskID string, kill func()) (unregister func()) {
	if taskID == "" {
		return func() {}
	}
	taskReg.mu.Lock()
	defer taskReg.mu.Unlock()
	taskReg.next++
	n := taskReg.next
	if taskReg.m[taskID] == nil {
		taskReg.m[taskID] = map[int]func(){}
	}
	taskReg.m[taskID][n] = kill
	return func() {
		taskReg.mu.Lock()
		defer taskReg.mu.Unlock()
		delete(taskReg.m[taskID], n)
		if len(taskReg.m[taskID]) == 0 {
			delete(taskReg.m, taskID)
		}
	}
}

// KillTask kills every running process started with Spec.TaskID == taskID. It
// does nothing if there is none.
func KillTask(taskID string) error {
	taskReg.mu.Lock()
	kills := make([]func(), 0, len(taskReg.m[taskID]))
	for _, k := range taskReg.m[taskID] {
		kills = append(kills, k)
	}
	taskReg.mu.Unlock()
	for _, k := range kills {
		k()
	}
	return nil
}
