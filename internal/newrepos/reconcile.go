package newrepos

import (
	"context"
	"crypto/sha1" //nolint:gosec // git object IDs are SHA-1; this is not a security use
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Reconciliation is one bounded, read-only question about a stage whose
// outcome is unknown. It never writes, never repeats the stage and never
// deletes. An exact match is the only answer that moves the operation forward.

const readRetries = 2

// check is one read-only look: what was found and, for an exact match, the
// receipt. A non-nil error is a transient read failure.
type check struct {
	outcome corenewrepos.ReconcileOutcome
	receipt corenewrepos.Receipt
}

func found(rc corenewrepos.Receipt) check {
	return check{outcome: corenewrepos.ReconcileExactSuccess, receipt: rc}
}

var (
	absent   = check{outcome: corenewrepos.ReconcileAbsent}
	conflict = check{outcome: corenewrepos.ReconcileConflict}
)

// runReconcile asks the question for an Uncertain operation. ok is false when
// the answer is unknown or the context ended: nothing is written and the
// operation stays Uncertain (the next start asks again).
func (s *Service) runReconcile(ctx context.Context, c corenewrepos.ReconcileStage) (corenewrepos.Reconciled, bool) {
	rec, err := s.d.Store.GetReservation(ctx, c.Operation)
	if err != nil {
		s.logErr("reconcile: load", err, "operation", c.Operation)
		return corenewrepos.Reconciled{}, false
	}
	st, ok := rec.Operation.State.(corenewrepos.Uncertain)
	if !ok || st.Attempt != c.Attempt || st.Stage != c.Stage {
		return corenewrepos.Reconciled{}, false
	}
	sctx, cancel := s.stageCtx(ctx)
	defer cancel()
	var res check
	for try := 0; ; try++ {
		res, err = s.check(sctx, rec, st)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return corenewrepos.Reconciled{}, false
		}
		s.logErr("reconcile: read failed", err, "operation", c.Operation, "stage", c.Stage.Name(), "try", try+1)
		if try >= readRetries {
			return corenewrepos.Reconciled{}, false // stay Uncertain
		}
		t := time.NewTimer(s.d.RetryDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return corenewrepos.Reconciled{}, false
		case <-t.C:
		}
	}
	if res.outcome == corenewrepos.ReconcileUnknown {
		return corenewrepos.Reconciled{}, false
	}
	return corenewrepos.Reconciled{Attempt: c.Attempt, Stage: c.Stage, Outcome: res.outcome, Receipt: res.receipt, Now: s.d.Clock()}, true
}

func (s *Service) check(ctx context.Context, rec db.ReservationRecord, st corenewrepos.Uncertain) (check, error) {
	switch r := st.Resume.(type) {
	case corenewrepos.ResumeInit:
		stage, ok := st.Stage.(corenewrepos.InitStage)
		if !ok {
			return conflict, nil
		}
		return s.checkInit(ctx, r.Spec, stage, r.Done)
	case corenewrepos.ResumePublish:
		stage, ok := st.Stage.(corenewrepos.PublishStage)
		if !ok {
			return conflict, nil
		}
		return s.checkPublish(ctx, rec, r.Spec, stage, r.Done)
	case corenewrepos.ResumeNone:
	}
	return conflict, nil
}

func (s *Service) checkInit(ctx context.Context, spec corenewrepos.InitSpec, stage corenewrepos.InitStage, done []corenewrepos.Receipt) (check, error) {
	parent, err := s.parentOf(ctx, spec.Project)
	if err != nil {
		return check{}, err
	}
	dir := filepath.Join(parent, spec.Leaf)
	if stage == corenewrepos.StageCreateDirectory {
		// No identity receipt exists for an unknown create, so a folder that is
		// there cannot be proven ours: it needs a human, never adoption.
		_, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return absent, nil
		case err != nil:
			return check{}, err
		}
		return conflict, nil
	}
	if stage == corenewrepos.StageFinalizeTracking {
		if _, err := s.d.Store.GetReservedRepo(ctx, spec.Repo); err == nil {
			return absent, nil // still reserved: the commit that makes it ready did not happen
		} else if errors.Is(err, db.ErrNotFound) {
			return conflict, nil
		} else {
			return check{}, err
		}
	}
	id, ok := dirIdentityOf(done)
	if !ok {
		return conflict, nil
	}
	if err := s.d.FS.VerifyChildDir(ctx, parent, spec.Leaf, id); err != nil {
		if errors.Is(err, platform.ErrChanged) || errors.Is(err, platform.ErrUnsafePath) {
			return conflict, nil //nolint:nilerr // a swapped or linked folder is a conflict, not a read failure
		}
		return check{}, err
	}
	switch stage {
	case corenewrepos.StageInitGit:
		return checkGitDir(dir, spec.Branch, id)
	case corenewrepos.StageWriteREADME:
		return s.checkFile(ctx, parent, spec.Leaf, id, "README.md", spec.README)
	case corenewrepos.StageWriteIgnore:
		return s.checkFile(ctx, parent, spec.Leaf, id, ".gitignore", spec.Ignore)
	case corenewrepos.StageFirstCommit:
		fc, found2, err := s.d.Git.ReadFirstCommit(ctx, dir, spec.Branch)
		switch {
		case errors.Is(err, git.ErrNotFirstCommit):
			return conflict, nil
		case err != nil:
			return check{}, err
		case !found2:
			return absent, nil
		}
		// Finding any HEAD is not proof: the tree, the message and the author
		// must be exactly what was confirmed, and there is no parent (the
		// reader refuses one).
		if fc.Tree != expectedTree(spec) || fc.MessageHash != git.MessageHash(spec.CommitMessage) || fc.Author == "" {
			return conflict, nil
		}
		return found(corenewrepos.CommitReceipt{Commit: fc.Commit, Tree: fc.Tree, MessageHash: fc.MessageHash, Author: fc.Author}), nil
	case corenewrepos.StageCreateDirectory, corenewrepos.StageFinalizeTracking:
	}
	return conflict, nil
}

// checkGitDir reads (never writes) the .git folder: a real folder, not a link,
// whose HEAD names the initial branch.
func checkGitDir(dir, branch string, id platform.DirIdentity) (check, error) {
	gitDir := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return absent, nil
	case err != nil:
		return check{}, err
	case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0:
		return conflict, nil
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")) //nolint:gosec // dir is the canonical project folder plus one validated leaf
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return absent, nil // git init did not get as far as HEAD
	case err != nil:
		return check{}, err
	}
	if string(head) != "ref: refs/heads/"+branch+"\n" {
		return conflict, nil
	}
	return found(corenewrepos.GitReceipt{GitDirIdentity: gitIdentity(id)}), nil
}

func (s *Service) checkFile(ctx context.Context, parent, leaf string, id platform.DirIdentity, name, content string) (check, error) {
	hash, exists, err := s.d.FS.FileHash(ctx, parent, leaf, id, name)
	switch {
	case err != nil:
		return check{}, err
	case !exists:
		return absent, nil
	case hash != fileHash(content):
		return conflict, nil
	}
	return found(corenewrepos.FileReceipt{Name: name, ContentHash: hash}), nil
}

func (s *Service) checkPublish(ctx context.Context, rec db.ReservationRecord, spec corenewrepos.PublishSpec,
	stage corenewrepos.PublishStage, done []corenewrepos.Receipt) (check, error) {
	switch stage {
	case corenewrepos.StageCreateRemote:
		// Adopt only an exact match: this operation's marker, the confirmed
		// owner and visibility, and no refs. Anything else is a conflict, never
		// a repository somebody else made.
		l, err := s.d.Remote.LookupRepo(ctx, createSpec(spec))
		if err != nil {
			return check{}, err
		}
		switch l.Outcome {
		case hosts.RepoExactMatch:
			if l.RemoteID == "" || l.URL == "" {
				return conflict, nil
			}
			return found(corenewrepos.RemoteReceipt{RemoteID: l.RemoteID, URL: l.URL}), nil
		case hosts.RepoAbsent:
			return absent, nil
		case hosts.RepoConflict:
			return conflict, nil
		}
		return check{outcome: corenewrepos.ReconcileUnknown}, nil
	case corenewrepos.StageAddOrigin:
		remote, ok := remoteOf(done)
		if !ok {
			return conflict, nil
		}
		_, dir, err := s.repoDir(ctx, rec)
		if err != nil {
			return check{}, err
		}
		cur, exists, err := s.d.Origin.Origin(ctx, dir)
		switch {
		case exists && err != nil:
			return conflict, nil //nolint:nilerr // an origin that is not a plain address is a conflict, not a read failure
		case err != nil:
			return check{}, err
		case !exists:
			return absent, nil
		}
		if want, verr := git.ValidateRemoteURL(remote.URL); verr == nil && cur == want {
			return found(corenewrepos.OriginReceipt{URL: remote.URL}), nil
		}
		return conflict, nil
	}
	return s.checkPush(ctx, rec, spec, stage, done)
}

func (s *Service) checkPush(ctx context.Context, rec db.ReservationRecord, spec corenewrepos.PublishSpec,
	stage corenewrepos.PublishStage, done []corenewrepos.Receipt) (check, error) {
	switch stage {
	case corenewrepos.StageCreateRemote, corenewrepos.StageAddOrigin:
		return conflict, nil
	case corenewrepos.StagePushHead:
		remote, ok := remoteOf(done)
		if !ok {
			return conflict, nil
		}
		_, dir, err := s.repoDir(ctx, rec)
		if err != nil {
			return check{}, err
		}
		ref, err := s.d.Transport.RemoteBranch(ctx, publishRequest(dir, remote.URL, spec))
		switch {
		case err != nil:
			return check{}, err
		case !ref.Exists:
			return absent, nil
		case ref.Head != spec.ExpectedHead:
			return conflict, nil
		}
		return found(corenewrepos.PushReceipt{Head: ref.Head}), nil
	}
	return conflict, nil
}

func publishRequest(dir, url string, spec corenewrepos.PublishSpec) git.PublishRequest {
	return git.PublishRequest{Dir: dir, URL: url, Branch: spec.Branch, ExpectedHead: spec.ExpectedHead}
}

func fileHash(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

// expectedTree is the git tree ID of exactly the two files of the first commit,
// computed the way git does (SHA-1 over the sorted entries of blob IDs), so an
// unknown first commit can be checked without trusting "some HEAD exists".
func expectedTree(spec corenewrepos.InitSpec) string {
	blob := func(content string) []byte {
		h := sha1.New() //nolint:gosec // see import
		fmt.Fprintf(h, "blob %d\x00%s", len(content), content)
		return h.Sum(nil)
	}
	// Entries sort by name bytes: ".gitignore" comes before "README.md".
	var body []byte
	for _, e := range []struct{ name, content string }{{".gitignore", spec.Ignore}, {"README.md", spec.README}} {
		body = append(body, fmt.Sprintf("100644 %s\x00", e.name)...)
		body = append(body, blob(e.content)...)
	}
	h := sha1.New() //nolint:gosec // see import
	fmt.Fprintf(h, "tree %d\x00", len(body))
	h.Write(body)
	return fmt.Sprintf("%x", h.Sum(nil))
}
