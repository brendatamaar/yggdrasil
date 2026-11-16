package repos_test

// HistoryReader contract (AU-02): generated scenarios run through the same
// clauses against a scripted HistoryGit and against real git on a temporary
// repo. The oracle is written from the design: author-time instants in a
// half-open UTC interval, newest first, equal times by ascending full hash,
// cut at max, truncated exactly when more matches existed.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type histRec struct {
	hash    string
	subject string
	at      time.Time // author time
}

// out runs git through platform.Start, like gitFx.run, and returns stdout.
func (f *gitFx) out(dir string, args ...string) string {
	f.t.Helper()
	var out, errb bytes.Buffer
	argv := append([]string{f.exe, "-c", "core.longpaths=true"}, args...)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: argv, Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		f.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		f.t.Fatalf("git %v: exit %d, err %v: %s", args, code, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func oracleHistory(recs []histRec, from, to time.Time, max int) ([]repos.HistoryCommit, bool) {
	var in []histRec
	for _, r := range recs {
		if !r.at.Before(from) && r.at.Before(to) {
			in = append(in, r)
		}
	}
	slices.SortFunc(in, func(a, b histRec) int {
		if !a.at.Equal(b.at) {
			if a.at.After(b.at) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.hash, b.hash)
	})
	truncated := len(in) > max
	if truncated {
		in = in[:max]
	}
	out := make([]repos.HistoryCommit, len(in))
	for i, r := range in {
		out[i] = repos.HistoryCommit{OID: r.hash, Subject: r.subject, Author: "Ada Test", At: r.at.UTC()}
	}
	return out, truncated
}

type histQuery struct {
	from, to time.Time
	max      int
}

// histScenario returns commits in creation order (not author-time order), with
// author times that tie and with authors in different time zones.
func histScenario(r *rand.Rand) []histRec {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n := 3 + r.Intn(10)
	var out []histRec
	for i := 0; i < n; i++ {
		out = append(out, histRec{subject: fmt.Sprintf("change %d", i), at: base.Add(time.Duration(r.Intn(20)) * time.Minute)})
	}
	return out
}

func histQueries(r *rand.Rand, recs []histRec) []histQuery {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	qs := []histQuery{
		{base.Add(-time.Hour), base.Add(time.Hour), 200},
		{base, base.Add(30 * time.Minute), 1},
		{base.Add(-31*24*time.Hour + time.Hour), base.Add(time.Hour), 3},
	}
	// Windows whose edges sit exactly on commit instants: included at the start,
	// excluded at the end.
	for _, rec := range recs[:min(3, len(recs))] {
		qs = append(qs, histQuery{rec.at, rec.at.Add(time.Minute), 200}, histQuery{rec.at.Add(-time.Minute), rec.at, 200}, histQuery{rec.at, rec.at.Add(time.Second), 2})
	}
	for i := 0; i < 5; i++ {
		from := base.Add(time.Duration(r.Intn(24)-2) * time.Minute)
		qs = append(qs, histQuery{from, from.Add(time.Duration(1+r.Intn(30)) * time.Minute), []int{1, 2, 3, 5, 200}[r.Intn(5)]})
	}
	// Around the real clock: no commit has an author time there, even though
	// the committer time of every live commit is "now".
	if now := time.Now().UTC(); now.After(base.Add(60 * 24 * time.Hour)) {
		qs = append(qs, histQuery{now.Add(-time.Hour), now.Add(time.Hour), 200})
	}
	return qs
}

func runHistoryClauses(t *testing.T, reader *repos.GitHistoryReader, id core.RepoID, recs []histRec, qs []histQuery) {
	t.Helper()
	for i, q := range qs {
		got, truncated, err := reader.Commits(context.Background(), id, q.from, q.to, q.max)
		if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		want, wantTruncated := oracleHistory(recs, q.from, q.to, q.max)
		if truncated != wantTruncated || len(got) != len(want) {
			t.Fatalf("query %d [%v, %v) max %d: got %d commits truncated=%v, want %d truncated=%v", i, q.from, q.to, q.max, len(got), truncated, len(want), wantTruncated)
		}
		for j := range want {
			if got[j].OID != want[j].OID || got[j].Subject != want[j].Subject || got[j].Author != want[j].Author || !got[j].At.Equal(want[j].At) || got[j].At.Location() != time.UTC {
				t.Fatalf("query %d commit %d: got %+v, want %+v", i, j, got[j], want[j])
			}
		}
	}
}

func TestPropHistoryReaderMatchesOracleFakeAndRealGit(t *testing.T) {
	fakeSeeds, liveSeeds := 150, 6
	if testing.Short() {
		fakeSeeds, liveSeeds = 20, 2
	}
	t.Run("scripted", func(t *testing.T) {
		for seed := int64(1); seed <= int64(fakeSeeds); seed++ {
			r := rand.New(rand.NewSource(seed))
			recs := histScenario(r)
			var commits []git.Commit
			for i := range recs {
				recs[i].hash = fmt.Sprintf("%040x", r.Uint64()>>(r.Intn(40))) // ties are decided by the hash
				commits = append(commits, git.Commit{Hash: recs[i].hash, Subject: recs[i].subject, Author: "Ada Test", Date: recs[i].at})
			}
			if hasDuplicateHash(recs) {
				continue
			}
			src := &memSource{}
			row := repoRow("api")
			src.set(row)
			runHistoryClauses(t, repos.NewHistoryReader(src, &historyGitFake{records: commits}), row.ID, recs, histQueries(r, recs))
			if t.Failed() {
				t.Fatalf("seed %d", seed)
			}
		}
	})
	t.Run("real git", func(t *testing.T) {
		for seed := int64(1); seed <= int64(liveSeeds); seed++ {
			r := rand.New(rand.NewSource(1000 + seed))
			recs := histScenario(r)
			fx := newGitFx(t)
			repo := fx.repo("shop-api")
			for i := range recs {
				// Authors in different zones: the same instant, written in local time.
				zone := time.FixedZone("z", []int{0, 7 * 3600, -5 * 3600, 9*3600 + 1800}[r.Intn(4)])
				fx.run(repo, "commit", "-q", "--allow-empty", "-m", recs[i].subject, "--date="+recs[i].at.In(zone).Format(time.RFC3339))
				recs[i].hash = fx.out(repo, "rev-parse", "HEAD")
			}
			src := &memSource{}
			row := repoRow("api")
			row.Path = repo
			src.set(row)
			runHistoryClauses(t, repos.NewHistoryReader(src, fx.g), row.ID, recs, histQueries(r, recs))
			if t.Failed() {
				t.Fatalf("seed %d", seed)
			}
		}
	})
}

func hasDuplicateHash(recs []histRec) bool {
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.hash] {
			return true
		}
		seen[r.hash] = true
	}
	return false
}

// The count cut keeps the newest author times even when git's own commit order
// (and every committer time) disagrees: 205 commits are created oldest-author
// first, newest-author last, in a scrambled order.
func TestHistoryCountCutKeepsNewestAuthorTimesNotLatestCommits(t *testing.T) {
	const n = 205
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recs := make([]histRec, n)
	for i := range recs {
		recs[i] = histRec{subject: fmt.Sprintf("change %03d", i), at: base.Add(time.Duration((i*37)%n) * time.Minute)}
	}
	var commits []git.Commit
	for i := range recs {
		recs[i].hash = fmt.Sprintf("%040x", i+1)
		commits = append(commits, git.Commit{Hash: recs[i].hash, Subject: recs[i].subject, Author: "Ada Test", Date: recs[i].at})
	}
	window := []histQuery{{base, base.Add(24 * time.Hour), 200}, {base, base.Add(24 * time.Hour), 1}, {base.Add(time.Minute), base.Add(206 * time.Minute), 200}}
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint("git=", live), func(t *testing.T) {
			src := &memSource{}
			row := repoRow("api")
			var g repos.HistoryGit = &historyGitFake{records: commits}
			if live {
				fx := newGitFx(t)
				repo := fx.repo("shop-api")
				for i := range recs {
					fx.run(repo, "commit", "-q", "--allow-empty", "-m", recs[i].subject, "--date="+recs[i].at.Format(time.RFC3339))
					recs[i].hash = fx.out(repo, "rev-parse", "HEAD")
				}
				row.Path, g = repo, fx.g
			}
			src.set(row)
			runHistoryClauses(t, repos.NewHistoryReader(src, g), row.ID, recs, window)
			got, truncated, err := repos.NewHistoryReader(src, g).Commits(context.Background(), row.ID, base, base.Add(24*time.Hour), 200)
			if err != nil || !truncated || len(got) != 200 {
				t.Fatalf("%d commits, truncated=%v, err %v", len(got), truncated, err)
			}
			if !got[0].At.Equal(base.Add(204*time.Minute)) || !got[199].At.Equal(base.Add(5*time.Minute)) {
				t.Fatalf("latest-only sampling: first %v, last %v", got[0].At, got[199].At)
			}
		})
	}
}

// Scripted facts that cannot be real are refused at the reader boundary and
// become "unavailable", never partial data. Each case alters one thing.
func TestHistoryReaderRejectsMalformedScriptedResults(t *testing.T) {
	from := historyFrom
	to := from.Add(24 * time.Hour)
	mk := func(i int, at time.Time) git.Commit {
		return git.Commit{Hash: fmt.Sprintf("%040x", i), Subject: "change", Author: "Ada Test", Date: at}
	}
	good := []git.Commit{mk(2, from.Add(2*time.Hour)), mk(1, from.Add(time.Hour))}
	sha256 := strings.Repeat("ab", 32)
	for name, recs := range map[string][]git.Commit{
		"short oid":              {{Hash: "abc", Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"41 hex digits":          {{Hash: strings.Repeat("a", 41), Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"63 hex digits":          {{Hash: strings.Repeat("a", 63), Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"65 hex digits":          {{Hash: strings.Repeat("a", 65), Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"uppercase oid":          {{Hash: strings.Repeat("A", 40), Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"oid with a space":       {{Hash: strings.Repeat("a", 39) + " ", Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"empty oid":              {{Hash: "", Subject: "s", Author: "a", Date: from.Add(time.Hour)}},
		"subject with a newline": {{Hash: mk(1, from).Hash, Subject: "a\nb", Author: "a", Date: from.Add(time.Hour)}},
		"subject with NUL":       {{Hash: mk(1, from).Hash, Subject: "a\x00b", Author: "a", Date: from.Add(time.Hour)}},
		"author with escape":     {{Hash: mk(1, from).Hash, Subject: "s", Author: "a\x1b[31m", Date: from.Add(time.Hour)}},
		"invalid UTF-8 subject":  {{Hash: mk(1, from).Hash, Subject: "a\xffb", Author: "a", Date: from.Add(time.Hour)}},
		"invalid UTF-8 author":   {{Hash: mk(1, from).Hash, Subject: "s", Author: "\xc3", Date: from.Add(time.Hour)}},
		"zero date":              {{Hash: mk(1, from).Hash, Subject: "s", Author: "a"}},
		"before the interval":    {mk(1, from.Add(-time.Second))},
		"at the exclusive end":   {mk(1, to)},
		"after the interval":     {mk(1, to.Add(time.Hour))},
		"duplicate hash":         {mk(1, from.Add(2*time.Hour)), mk(1, from.Add(time.Hour))},
		"oldest first":           {mk(1, from.Add(time.Hour)), mk(2, from.Add(2*time.Hour))},
		"tie, hash descending":   {mk(2, from.Add(time.Hour)), mk(1, from.Add(time.Hour))},
		"duplicate hash apart":   {mk(3, from.Add(3*time.Hour)), mk(1, from.Add(2*time.Hour)), mk(3, from.Add(time.Hour))},
	} {
		t.Run(name, func(t *testing.T) {
			src := &memSource{}
			row := repoRow("api")
			src.set(row)
			cs, partial, err := repos.NewHistoryReader(src, &historyGitFake{records: recs, raw: true}).Commits(context.Background(), row.ID, from, to, 200)
			if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil || partial {
				t.Fatalf("malformed result accepted: %+v partial=%v err=%v", cs, partial, err)
			}
		})
	}
	t.Run("more commits than requested", func(t *testing.T) {
		src := &memSource{}
		row := repoRow("api")
		src.set(row)
		cs, _, err := repos.NewHistoryReader(src, &historyGitFake{records: good, raw: true}).Commits(context.Background(), row.ID, from, to, 1)
		if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil {
			t.Fatalf("%+v %v", cs, err)
		}
	})
	t.Run("total size over 1 MiB", func(t *testing.T) {
		src := &memSource{}
		row := repoRow("api")
		src.set(row)
		a, b := mk(2, from.Add(2*time.Hour)), mk(1, from.Add(time.Hour))
		a.Subject, b.Subject = strings.Repeat("é", 262144), strings.Repeat("é", 262144) // 2 x 512 KiB
		cs, _, err := repos.NewHistoryReader(src, &historyGitFake{records: []git.Commit{a, b}, raw: true}).Commits(context.Background(), row.ID, from, to, 200)
		if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil {
			t.Fatalf("%d commits, %v", len(cs), err)
		}
	})
	// Well-formed results of both hash widths, in any zone, are returned in UTC.
	t.Run("valid SHA-1 and SHA-256 facts", func(t *testing.T) {
		src := &memSource{}
		row := repoRow("api")
		src.set(row)
		zone := time.FixedZone("z", 7*3600)
		recs := []git.Commit{{Hash: sha256, Subject: "日本語 subject", Author: "Ada Test", Date: from.Add(3 * time.Hour).In(zone)}, good[0], good[1]}
		cs, partial, err := repos.NewHistoryReader(src, &historyGitFake{records: recs, raw: true}).Commits(context.Background(), row.ID, from, to, 200)
		if err != nil || partial || len(cs) != 3 || cs[0].OID != sha256 || cs[0].At.Location() != time.UTC || !cs[0].At.Equal(from.Add(3*time.Hour)) {
			t.Fatalf("%+v partial=%v err=%v", cs, partial, err)
		}
	})
}

// Returned slices belong to the caller; concurrent readers never share storage.
func TestHistoryResultsAreNotAliasedAcrossCalls(t *testing.T) {
	from := historyFrom
	to := from.Add(24 * time.Hour)
	src := &memSource{}
	row := repoRow("api")
	src.set(row)
	g := &historyGitFake{records: historyRows()}
	reader := repos.NewHistoryReader(src, g)
	first, _, err := reader.Commits(context.Background(), row.ID, from, to, 200)
	if err != nil || len(first) == 0 {
		t.Fatal(first, err)
	}
	want := slices.Clone(first)
	first[0].Subject = "caller mutation"
	second, _, err := reader.Commits(context.Background(), row.ID, from, to, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("results alias between calls: %+v, want %+v", second, want)
	}
}

// A cancelled or already-expired context makes no git call at all, and a
// cancellation during the read returns no data.
func TestHistoryCancellationMakesNoCallOrReturnsNothing(t *testing.T) {
	src := &memSource{}
	row := repoRow("api")
	src.set(row)
	g := &historyGitFake{records: historyRows()}
	reader := repos.NewHistoryReader(src, g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cs, partial, err := reader.Commits(ctx, row.ID, historyFrom, historyFrom.Add(time.Hour), 10)
	if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil || partial || g.calls != 0 {
		t.Fatalf("pre-cancelled: %+v %v calls=%d", cs, err, g.calls)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	g.after = cancel2
	cs, partial, err = reader.Commits(ctx2, row.ID, historyFrom, historyFrom.Add(24*time.Hour), 10)
	if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil || partial {
		t.Fatalf("cancelled during read: %+v %v", cs, err)
	}
}
