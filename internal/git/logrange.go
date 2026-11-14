package git

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	logRangeDays        = 31 * 24 * time.Hour
	logRangeRecords     = 200
	logRangeBytes       = 1 << 20
	logRangeStderrBytes = 16 << 10
	logRangeTimeout     = 5 * time.Second
)

// LogRange reads HEAD history for AU-02/AC-04. Date is the author timestamp
// (%aI), as in Log, normalized to UTC. The interval is [from,to), without
// rounding; it must be nonzero, increasing and at most 31 days, with max in
// [1,200]. Results sort by Date descending, then full hash ascending.
//
// The walk deliberately has no Git date or count filter: committer order/dates
// cannot establish completeness for author dates. It captures at most 1 MiB
// stdout and 16 KiB stderr under a five-second context. Truncated means either
// more matching records exist or the byte/pipe bound stopped the history walk (even
// if no matches were seen). In that case results are only the best observed
// matches, not necessarily the newest in the entire history; callers must label
// them partial. An incomplete final record at cutoff is ignored. Malformed
// complete records and process/cancellation failures return *Error, never data.
// Empty/unborn repositories succeed with an empty result.
func (g *Git) LogRange(ctx context.Context, dir string, from, to time.Time, max int) ([]Commit, bool, error) {
	from, to = from.UTC(), to.UTC()
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > logRangeDays || max < 1 || max > logRangeRecords {
		return nil, false, &Error{Op: "log range", Err: ErrInvalidArg, UserMsg: "Choose an increasing date range of at most 31 days and between 1 and 200 records."}
	}
	ctx, cancel := context.WithTimeout(ctx, logRangeTimeout)
	defer cancel()
	out, cut, err := g.runCaptured(ctx, dir, "log range", true, logRangeBytes, logRangeStderrBytes,
		"log", "-z", "--no-show-signature", "--format="+commitFormat, "--")
	if err != nil {
		var ge *Error
		if ctx.Err() == nil && errors.As(err, &ge) && strings.Contains(ge.Stderr, "does not have any commits") {
			return nil, false, nil
		}
		return nil, false, err
	}
	commits, truncated, err := parseLogRange(out, cut, from, to, max)
	if ctx.Err() != nil {
		return nil, false, &Error{Op: "log range", Err: ctx.Err(), UserMsg: "Git history reading was cancelled. Try again."}
	}
	if err != nil {
		return nil, false, &Error{Op: "log range", Err: err, UserMsg: "Yggdrasil could not read the output of git log. Update git and try again."}
	}
	return commits, truncated, nil
}

// parseLogRange validates complete records before filtering; bounded insertion
// retains only max+1 matches, independent of the number of scanned records.
func parseLogRange(out string, cut bool, from, to time.Time, max int) ([]Commit, bool, error) {
	truncated := cut
	var res []Commit
	for out != "" {
		rec, rest, terminated := strings.Cut(out, "\x00")
		if !terminated {
			if cut {
				break
			}
			return nil, false, fmt.Errorf("unterminated history record")
		}
		out = rest
		// A separator inside a subject/identity is ambiguous: fail closed.
		if strings.Count(rec, "\x1f") != 6 {
			return nil, false, fmt.Errorf("invalid history fields")
		}
		c, _, err := parseCommit(rec)
		if err != nil {
			return nil, false, err
		}
		if !validOID(c.Hash) || !strings.HasPrefix(c.Hash, c.Short) || c.Short == "" {
			return nil, false, fmt.Errorf("invalid history identity")
		}
		for _, parent := range c.Parents {
			if !validOID(parent) {
				return nil, false, fmt.Errorf("invalid history parent")
			}
		}
		c.Date = c.Date.UTC()
		if c.Date.Before(from) || !c.Date.Before(to) {
			continue
		}
		res = append(res, c)
		sort.Slice(res, func(i, j int) bool {
			if res[i].Date.Equal(res[j].Date) {
				return res[i].Hash < res[j].Hash
			}
			return res[i].Date.After(res[j].Date)
		})
		if len(res) > max {
			truncated = true
			res = res[:max]
		}
	}
	return res, truncated, nil
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
