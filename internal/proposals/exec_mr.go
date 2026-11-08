package proposals

import (
	"context"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/changesets"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// SetLinks is what the MR executor needs to know about the change set of the
// proposal it runs (RV-03, docs/design/changesets.md).
type SetLinks struct {
	// Order is the set's repos in merge order (RV-04).
	Order []core.RepoID
	// Branches is the MR branch of each repo that has an MR proposal.
	Branches map[core.RepoID]string
	// URLs holds the MR URL of each repo whose merge_request proposal is
	// executed. Only executed members belong here.
	URLs map[core.RepoID]string
}

// ChangeSetLinks reads the linked-MR facts of a change set. The change-set
// store implements it; a nil value in Config means no links.
type ChangeSetLinks interface {
	MRLinks(ctx context.Context, set core.ChangeSetID) (SetLinks, error)
}

// validateMRDescription is the Create-time check of the confirmed template.
func validateMRDescription(desc string) string {
	if err := changesets.ValidateLinkMarker(desc); err != nil {
		return "The merge request description cannot be used: " + err.Error() + "."
	}
	return ""
}

// earlierLinks keeps only the URLs of repos that come before repo in order:
// links point backwards, and a repo never links to itself.
func earlierLinks(sl SetLinks, repo core.RepoID) map[core.RepoID]string {
	out := map[core.RepoID]string{}
	for _, r := range sl.Order {
		if r == repo {
			return out
		}
		if u, ok := sl.URLs[r]; ok {
			out[r] = u
		}
	}
	return map[core.RepoID]string{} // repo not in the order: link nothing
}

// fillLinks replaces the marker of the confirmed template with the links to
// earlier MRs of the set. It returns the description to push and a note for
// the result ("" when the template has no marker). A read failure is not fatal:
// the marker is filled with the repo and branch names only.
func (s *Service) fillLinks(ctx context.Context, p coreprop.Proposal, template, host string) (string, string) {
	if !strings.Contains(template, changesets.LinkMarker) || changesets.ValidateLinkMarker(template) != nil {
		return template, ""
	}
	var sl SetLinks
	if s.cfg.Links != nil {
		got, err := s.cfg.Links.MRLinks(ctx, p.ChangeSet)
		if err != nil {
			s.cfg.Logger.Warn("read change set links", "proposal", p.ID, "error", s.redact(err.Error()))
		} else {
			sl = got
		}
	}
	links := earlierLinks(sl, p.Repo)
	order := sl.Order
	if len(order) == 0 {
		order = []core.RepoID{p.Repo}
	}
	desc := s.redact(changesets.RenderLinks(template, order, sl.Branches, links, host))
	text := changesets.RenderLinks(changesets.LinkMarker, order, sl.Branches, links, host)
	return desc, s.redact(fmt.Sprintf(" Links added to the description: %s", text))
}

// execMR pushes the branch with GitLab merge-request push options (GH-03). The
// description is one line because push options cannot carry a newline. The
// confirmed (hash-covered) text is a template; the link marker is filled at
// execution time.
func (s *Service) execMR(ctx context.Context, p coreprop.Proposal, a coreprop.MergeRequest) outcome {
	return s.runPush(ctx, p, pushPlan{
		where: a.Where, branch: a.Branch, remote: a.Remote, expectHead: a.ExpectHead,
		mr:       &git.MROptions{Target: a.TargetBranch, Title: a.Title, Description: a.Description},
		template: a.Description,
	})
}
