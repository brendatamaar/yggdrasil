package standup

import (
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// PolishKey is the stable identity of one polish request: its project, range,
// agent, model and request ID. It does not contain the summary text, which is
// rebuilt from live sources and changes whenever a commit lands, so a retry of
// the same request keeps the same key. The owning package digests it with the
// task submission, so a repeat of the same request returns the first task and a
// different request under the same ID is refused.
func PolishKey(r Range, agent, model string, id core.RequestID) string {
	return strings.Join([]string{
		"standup-polish/v1", string(r.Project),
		fmt.Sprint(r.From.UnixNano()), fmt.Sprint(r.To.UnixNano()),
		fmt.Sprintf("%q", agent), fmt.Sprintf("%q", model), string(id),
	}, "|")
}
