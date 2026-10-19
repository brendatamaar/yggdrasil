package repos

import (
	"fmt"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// Boundary parsers of the git actions and of open in editor (WS-06 to WS-08,
// docs/design/git-actions.md). Each turns client values into a type that
// cannot hold an invalid value, and rejects the rest in plain language.

// BatchRequest is a validated request to start a batch.
type BatchRequest struct {
	Kind   corerepos.ActionKind
	Branch string // "" for fetch and pull
	Repos  []core.RepoID
}

// ParseBatchRequest accepts a known kind, a valid branch name exactly when
// the kind needs one, and 1 to 500 distinct valid repo IDs (exactly one for a
// checkout). Whether the repos are tracked is checked by the service.
func ParseBatchRequest(kind string, repoIDs []string, branch string) (BatchRequest, error) {
	k := corerepos.ActionKind(kind)
	if err := k.Validate(); err != nil {
		return BatchRequest{}, detailInvalid(`The action must be "fetch", "pull", "checkout" or "switch".`, "kind "+strconv.Quote(kind))
	}
	req := BatchRequest{Kind: k}
	switch {
	case k.NeedsBranch() && branch == "":
		return BatchRequest{}, detailInvalid("Choose a branch.", "branch missing")
	case !k.NeedsBranch() && branch != "":
		return BatchRequest{}, detailInvalid("Fetch and pull take no branch.", "branch given for "+kind)
	case k.NeedsBranch():
		ref, err := ParseRef(branch)
		if err != nil {
			return BatchRequest{}, err
		}
		req.Branch = ref.String()
	}
	switch {
	case len(repoIDs) < 1:
		return BatchRequest{}, detailInvalid("Choose at least one repo.", "no repos")
	case len(repoIDs) > corerepos.MaxBatchRepos:
		return BatchRequest{}, detailInvalid(fmt.Sprintf("One batch holds at most %d repos.", corerepos.MaxBatchRepos), "too many repos")
	case k == corerepos.KindCheckout && len(repoIDs) != 1:
		return BatchRequest{}, detailInvalid("A checkout works on one repo. Use switch for several.", "checkout with several repos")
	}
	seen := make(map[core.RepoID]struct{}, len(repoIDs))
	for _, s := range repoIDs {
		id := core.RepoID(s)
		if err := id.Validate(); err != nil {
			return BatchRequest{}, detailInvalid("One of the repos isn't valid. Refresh the page.", "repo ID "+strconv.Quote(s))
		}
		if _, dup := seen[id]; dup {
			return BatchRequest{}, detailInvalid("A repo is listed twice.", "duplicate repo "+s)
		}
		seen[id] = struct{}{}
		req.Repos = append(req.Repos, id)
	}
	return req, nil
}

// Format returns the values ParseBatchRequest accepts for r.
func (r BatchRequest) Format() (kind string, repoIDs []string, branch string) {
	for _, id := range r.Repos {
		repoIDs = append(repoIDs, string(id))
	}
	return string(r.Kind), repoIDs, r.Branch
}

// Editor is one of the editors Yggdrasil can open. The empty Editor means
// "use the default".
type Editor string

// The closed set of editors.
const (
	EditorVSCode Editor = "vscode"
	EditorCursor Editor = "cursor"
	EditorIdea   Editor = "idea"
)

// Editors lists the closed set, in the order they are tried when detecting.
var Editors = []Editor{EditorVSCode, EditorCursor, EditorIdea}

// Program is the executable name looked up on the PATH.
func (e Editor) Program() string {
	switch e {
	case EditorVSCode:
		return "code"
	case EditorCursor:
		return "cursor"
	case EditorIdea:
		return "idea"
	}
	return ""
}

// ParseEditor accepts "" (the default) and the closed set.
func ParseEditor(s string) (Editor, error) {
	if s == "" {
		return "", nil
	}
	for _, e := range Editors {
		if string(e) == s {
			return e, nil
		}
	}
	return "", detailInvalid(`The editor must be "vscode", "cursor" or "idea".`, "editor "+strconv.Quote(s))
}

// OpenRequest is a validated open or reveal request.
type OpenRequest struct {
	Editor Editor  // "" means the default
	File   RelPath // "" means the repo folder
}

// ParseOpen accepts an editor from the closed set (or none) and a file path
// inside the repo: relative, clean, without "..", without control characters
// (NUL included).
func ParseOpen(editor, file string) (OpenRequest, error) {
	e, err := ParseEditor(editor)
	if err != nil {
		return OpenRequest{}, err
	}
	f, err := ParseRelPath(file)
	if err != nil {
		return OpenRequest{}, err
	}
	return OpenRequest{Editor: e, File: f}, nil
}
