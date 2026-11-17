package newrepos

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// This file runs one stage effect. Every function here is called only for a
// command the core Step returned after its intent was committed. A mutating
// local stage holds the shared main lock around its own port calls only. No
// code in this package deletes a file or a folder, and no stage is repeated
// after an unknown outcome.

func dirIdentityOf(done []corenewrepos.Receipt) (platform.DirIdentity, bool) {
	for _, r := range done {
		if d, ok := r.(corenewrepos.DirReceipt); ok {
			return platform.DirIdentity(d.Identity), true
		}
	}
	return "", false
}

func remoteOf(done []corenewrepos.Receipt) (corenewrepos.RemoteReceipt, bool) {
	for _, r := range done {
		if v, ok := r.(corenewrepos.RemoteReceipt); ok {
			return v, true
		}
	}
	return corenewrepos.RemoteReceipt{}, false
}

// lock takes the shared main lock for one stage. Failing to get it means
// nothing ran, so the failure is definite.
func (s *Service) lock(ctx context.Context, repo core.RepoID) (func(), error) {
	release, err := s.d.Lock.Lock(ctx, repo)
	if err != nil {
		return nil, definiteErr(corenewrepos.StopPreconditions, fmt.Errorf("main checkout is busy: %w", err))
	}
	return release, nil
}

// verifyDir proves the folder is still the one this operation created. It only
// reads, so a failure is definite.
func (s *Service) verifyDir(ctx context.Context, parent, leaf string, id platform.DirIdentity) error {
	if err := s.d.FS.VerifyChildDir(ctx, parent, leaf, id); err != nil {
		return definiteErr(corenewrepos.StopPreconditions, err)
	}
	return nil
}

func (s *Service) stageCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.d.StageTimeout)
}

// runInit performs one init stage and returns the input for Step.
func (s *Service) runInit(ctx context.Context, c corenewrepos.RunInitStage) corenewrepos.Input {
	sctx, cancel := s.stageCtx(ctx)
	defer cancel()
	rc, err := s.execInit(sctx, c)
	now := s.d.Clock()
	if err != nil {
		s.logErr("new repo stage did not complete", err, "operation", c.Operation, "stage", c.Stage.Name())
		out, reason := verdict(err)
		return corenewrepos.StageFailed{Attempt: c.Attempt, Stage: c.Stage, Outcome: out, Reason: reason, Now: now}
	}
	return corenewrepos.StageSucceeded{Attempt: c.Attempt, Stage: c.Stage, Receipt: rc, Now: now}
}

func (s *Service) execInit(ctx context.Context, c corenewrepos.RunInitStage) (corenewrepos.Receipt, error) {
	spec := c.Spec
	parent, err := s.parentOf(ctx, spec.Project)
	if err != nil {
		return nil, err
	}
	if err := s.sameParent(ctx, spec, parent); err != nil {
		return nil, err
	}
	dir := filepath.Join(parent, spec.Leaf)
	switch c.Stage {
	case corenewrepos.StageCreateDirectory:
		// A missing git identity is refused before any disk effect.
		switch ok, aerr := s.d.Git.HasAuthor(ctx, parent); {
		case aerr != nil:
			return nil, definiteErr(corenewrepos.StopPreconditions, aerr)
		case !ok:
			return nil, definiteErr(corenewrepos.StopMissingAuthor, git.ErrNoAuthor)
		}
		release, err := s.lock(ctx, spec.Repo)
		if err != nil {
			return nil, err
		}
		defer release()
		id, err := s.d.FS.CreateChildDir(ctx, parent, spec.Leaf)
		if err != nil {
			return nil, classifyFS(err)
		}
		return corenewrepos.DirReceipt{Identity: string(id)}, nil
	case corenewrepos.StageInitGit:
		id, err := s.needDir(c.Done)
		if err != nil {
			return nil, err
		}
		release, err := s.lock(ctx, spec.Repo)
		if err != nil {
			return nil, err
		}
		defer release()
		if err := s.verifyDir(ctx, parent, spec.Leaf, id); err != nil {
			return nil, err
		}
		if err := s.d.Git.InitRepo(ctx, dir, spec.Branch); err != nil {
			return nil, classifyGit(err)
		}
		return corenewrepos.GitReceipt{GitDirIdentity: gitIdentity(id)}, nil
	case corenewrepos.StageWriteREADME:
		return s.writeFile(ctx, c, parent, "README.md", spec.README)
	case corenewrepos.StageWriteIgnore:
		return s.writeFile(ctx, c, parent, ".gitignore", spec.Ignore)
	case corenewrepos.StageFirstCommit:
		id, err := s.needDir(c.Done)
		if err != nil {
			return nil, err
		}
		release, err := s.lock(ctx, spec.Repo)
		if err != nil {
			return nil, err
		}
		defer release()
		if err := s.verifyDir(ctx, parent, spec.Leaf, id); err != nil {
			return nil, err
		}
		fc, err := s.d.Git.CommitFirst(ctx, dir, []string{"README.md", ".gitignore"}, spec.CommitMessage)
		if err != nil {
			return nil, classifyGit(err)
		}
		return corenewrepos.CommitReceipt{Commit: fc.Commit, Tree: fc.Tree, MessageHash: fc.MessageHash, Author: fc.Author}, nil
	case corenewrepos.StageFinalizeTracking:
		// The repo becomes ready in the commit that records this receipt
		// (apply). Here only check that the reservation is still there.
		if _, err := s.d.Store.GetReservedRepo(ctx, spec.Repo); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, definiteErr(corenewrepos.StopUntracked, err)
			}
			return nil, definiteErr(corenewrepos.StopPreconditions, err)
		}
		return corenewrepos.TrackedReceipt{}, nil
	}
	return nil, definiteErr(corenewrepos.StopNotImplemented, fmt.Errorf("unknown init stage %q", c.Stage))
}

// sameParent refuses a project folder that is not the one the reservation was
// made under: a root swapped for a link or another folder between the preview
// and the execution is a precondition failure, before anything is written.
func (s *Service) sameParent(ctx context.Context, spec corenewrepos.InitSpec, parent string) error {
	r, err := s.d.Store.GetReservedRepo(ctx, spec.Repo)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return definiteErr(corenewrepos.StopUntracked, err)
		}
		return definiteErr(corenewrepos.StopPreconditions, err)
	}
	was := filepath.Dir(r.Path)
	if was != parent && (!s.d.Paths.FoldCase() || !strings.EqualFold(was, parent)) {
		return definiteErr(corenewrepos.StopPreconditions, errors.New("the project folder changed since the reservation"))
	}
	return nil
}

func (s *Service) needDir(done []corenewrepos.Receipt) (platform.DirIdentity, error) {
	id, ok := dirIdentityOf(done)
	if !ok {
		return "", definiteErr(corenewrepos.StopConflict, errors.New("no folder identity was recorded"))
	}
	return id, nil
}

// gitIdentity binds the git receipt to the identity of the folder it was made
// in; reconcile also checks the .git folder itself.
func gitIdentity(dir platform.DirIdentity) string { return "git@" + string(dir) }

func (s *Service) writeFile(ctx context.Context, c corenewrepos.RunInitStage, parent, name, content string) (corenewrepos.Receipt, error) {
	id, err := s.needDir(c.Done)
	if err != nil {
		return nil, err
	}
	release, err := s.lock(ctx, c.Spec.Repo)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.verifyDir(ctx, parent, c.Spec.Leaf, id); err != nil {
		return nil, err
	}
	// Exclusive create: something already there is never overwritten.
	hash, err := s.d.FS.CreateFile(ctx, parent, c.Spec.Leaf, id, name, []byte(content))
	if err != nil {
		return nil, classifyFS(err)
	}
	return corenewrepos.FileReceipt{Name: name, ContentHash: hash}, nil
}

// runPublish performs one publish stage and returns the input for Step.
func (s *Service) runPublish(ctx context.Context, c corenewrepos.RunPublishStage) corenewrepos.Input {
	sctx, cancel := s.stageCtx(ctx)
	defer cancel()
	rc, err := s.execPublish(sctx, c)
	now := s.d.Clock()
	if err != nil {
		s.logErr("new repo stage did not complete", err, "operation", c.Operation, "stage", c.Stage.Name())
		out, reason := verdict(err)
		return corenewrepos.StageFailed{Attempt: c.Attempt, Stage: c.Stage, Outcome: out, Reason: reason, Now: now}
	}
	return corenewrepos.StageSucceeded{Attempt: c.Attempt, Stage: c.Stage, Receipt: rc, Now: now}
}

// publishTarget loads the operation's folder: the repo's parent, its path and
// the identity of the folder this operation created.
func (s *Service) publishTarget(ctx context.Context, op core.OperationID) (parent, leaf, dir string, id platform.DirIdentity, err error) {
	rec, err := s.d.Store.GetReservation(ctx, op)
	if err != nil {
		return "", "", "", "", definiteErr(corenewrepos.StopPreconditions, err)
	}
	parent, dir, err = s.repoDir(ctx, rec)
	if err != nil {
		return "", "", "", "", err
	}
	effects, err := s.d.Store.ListEffectsByOperation(ctx, op)
	if err != nil {
		return "", "", "", "", definiteErr(corenewrepos.StopPreconditions, err)
	}
	for _, e := range effects {
		if e.Stage == string(corenewrepos.StageCreateDirectory) && e.State == db.EffectSucceeded {
			r, derr := db.DecodeReceipt(string(e.Receipt))
			if d, ok := r.(corenewrepos.DirReceipt); ok && derr == nil {
				return parent, rec.Leaf, dir, platform.DirIdentity(d.Identity), nil
			}
		}
	}
	return "", "", "", "", definiteErr(corenewrepos.StopPreconditions, errors.New("no record of the folder this operation created"))
}

func createSpec(spec corenewrepos.PublishSpec) hosts.CreateRepoSpec {
	return hosts.CreateRepoSpec{
		Instance: hostInstance(spec), Owner: spec.Owner, OwnerKind: string(spec.OwnerKind), Name: spec.Name,
		Description: spec.Description(), Private: spec.Visibility == corenewrepos.VisibilityPrivate,
	}
}

func (s *Service) execPublish(ctx context.Context, c corenewrepos.RunPublishStage) (corenewrepos.Receipt, error) {
	spec := c.Spec
	switch c.Stage {
	case corenewrepos.StageCreateRemote:
		// One POST, never retried. The marker in the description was recorded
		// with the intent, so a lost reply can be found again.
		created, err := s.d.Remote.CreateRepo(ctx, createSpec(spec))
		if err != nil {
			return nil, classifyHost(err)
		}
		return corenewrepos.RemoteReceipt{RemoteID: created.RemoteID, URL: created.URL}, nil
	case corenewrepos.StageAddOrigin:
		remote, ok := remoteOf(c.Done)
		if !ok {
			return nil, definiteErr(corenewrepos.StopConflict, errors.New("no remote was recorded"))
		}
		parent, leaf, dir, id, err := s.publishTarget(ctx, c.Operation)
		if err != nil {
			return nil, err
		}
		release, err := s.lock(ctx, spec.Repo)
		if err != nil {
			return nil, err
		}
		defer release()
		if err := s.verifyDir(ctx, parent, leaf, id); err != nil {
			return nil, err
		}
		// Adds origin only while it is absent (or already exactly this URL).
		if err := s.d.Origin.AddOrigin(ctx, dir, remote.URL); err != nil {
			return nil, classifyGit(err)
		}
		return corenewrepos.OriginReceipt{URL: remote.URL}, nil
	case corenewrepos.StagePushHead:
		remote, ok := remoteOf(c.Done)
		if !ok {
			return nil, definiteErr(corenewrepos.StopConflict, errors.New("no remote was recorded"))
		}
		parent, leaf, dir, id, err := s.publishTarget(ctx, c.Operation)
		if err != nil {
			return nil, err
		}
		if err := s.verifyDir(ctx, parent, leaf, id); err != nil {
			return nil, err
		}
		// The in-process transport pushes exactly one branch at the expected
		// head, without force. The token stays inside the host service.
		res, err := s.d.Transport.PublishBranch(ctx, publishRequest(dir, remote.URL, spec))
		if err != nil {
			return nil, classifyPush(err)
		}
		return corenewrepos.PushReceipt{Head: res.Head}, nil
	}
	return nil, definiteErr(corenewrepos.StopNotImplemented, fmt.Errorf("unknown publish stage %q", c.Stage))
}
