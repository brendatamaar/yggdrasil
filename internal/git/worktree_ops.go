package git

import (
	"context"
	"strings"
)

// ResolveCommit returns the full commit ID that ref (a branch, tag or "HEAD";
// HEAD when empty) points to in the repo at dir. It reads only.
func (g *Git) ResolveCommit(ctx context.Context, dir, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if err := checkName("ref", ref); err != nil {
		return "", err
	}
	out, err := g.run(ctx, dir, "rev-parse", true, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
