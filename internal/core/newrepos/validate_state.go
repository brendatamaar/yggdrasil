package newrepos

import (
	"fmt"
	"strconv"
	"strings"
)

// ValidateReceipt checks the evidence used by stage recovery. A sealed variant
// alone does not prove that its required identities and hashes are present.
func ValidateReceipt(r Receipt) error {
	bad := func() error { return fmt.Errorf("invalid recovery receipt %T", r) }
	object := func(s string) bool { return (len(s) == 40 || len(s) == 64) && isLowerHex(s) }
	digest := func(s string) bool { return len(s) == 64 && isLowerHex(s) }
	text := func(s string) bool { return s != "" && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n") }
	switch v := r.(type) {
	case DirReceipt:
		if !text(v.Identity) {
			return bad()
		}
	case GitReceipt:
		if !text(v.GitDirIdentity) {
			return bad()
		}
	case FileReceipt:
		if (v.Name != "README.md" && v.Name != ".gitignore") || !digest(v.ContentHash) {
			return bad()
		}
	case CommitReceipt:
		if !object(v.Commit) || !object(v.Tree) || !digest(v.MessageHash) || !text(v.Author) {
			return bad()
		}
	case TrackedReceipt:
	case RemoteReceipt:
		if !text(v.RemoteID) || ValidateRemoteURL(v.URL) != nil {
			return bad()
		}
	case OriginReceipt:
		if ValidateRemoteURL(v.URL) != nil {
			return bad()
		}
	case PushReceipt:
		if !object(v.Head) {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

// ValidateRemoteURL excludes credentials, token query strings and fragments from
// recovery evidence. Remote credentials belong only in server HTTP headers.
func ValidateRemoteURL(raw string) error {
	rest, ok := strings.CutPrefix(raw, "https://")
	host, path, slash := strings.Cut(rest, "/")
	invalid := !ok || !slash || path == "" || host == "" || strings.ContainsAny(raw, "@?#%\\") || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f })
	if h, port, found := strings.Cut(host, ":"); found {
		n, err := strconv.Atoi(port)
		invalid = invalid || h == "" || err != nil || n < 1 || n > 65535
	}
	if invalid {
		return fmt.Errorf("a remote receipt needs a credential-free HTTPS URL without a query or fragment")
	}
	return nil
}

func validatePrefix(stage Stage, receipts []Receipt) error {
	var kinds []Receipt
	var index int
	switch v := stage.(type) {
	case InitStage:
		kinds = []Receipt{DirReceipt{}, GitReceipt{}, FileReceipt{Name: "README.md"}, FileReceipt{Name: ".gitignore"}, CommitReceipt{}, TrackedReceipt{}}
		index = -1
		for i, s := range InitStages() {
			if s == v {
				index = i
				break
			}
		}
	case PublishStage:
		kinds = []Receipt{RemoteReceipt{}, OriginReceipt{}, PushReceipt{}}
		index = -1
		for i, s := range PublishStages() {
			if s == v {
				index = i
				break
			}
		}
	default:
		return fmt.Errorf("recovery state has no stage")
	}
	if index < 0 || len(receipts) != index {
		return fmt.Errorf("recovery receipts do not match the completed stage prefix")
	}
	for i, r := range receipts {
		if err := ValidateReceipt(r); err != nil {
			return err
		}
		if receiptKind(r) != receiptKind(kinds[i]) {
			return fmt.Errorf("recovery receipt %d is for a different stage", i)
		}
		if f, ok := r.(FileReceipt); ok && f.Name != kinds[i].(FileReceipt).Name {
			return fmt.Errorf("recovery file receipt is out of order")
		}
	}
	return nil
}

// ValidateState binds decoded and persisted operation state to its immutable
// reservation spec and forbids missing or reordered stage evidence.
func ValidateState(spec InitSpec, st State) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	sameInit := func(s InitSpec) error {
		if s != spec {
			return fmt.Errorf("initialization state names a different reservation")
		}
		return nil
	}
	samePublish := func(s PublishSpec) error {
		if err := s.Validate(); err != nil {
			return err
		}
		if s.Repo != spec.Repo {
			return fmt.Errorf("publish state names a different repo")
		}
		return nil
	}
	checkResume := func(stage Stage, r Resumable) error {
		switch v := r.(type) {
		case ResumeNone:
			if stage != nil {
				return fmt.Errorf("a stage needs its original resume specification")
			}
		case ResumeInit:
			if err := sameInit(v.Spec); err != nil {
				return err
			}
			if _, ok := stage.(InitStage); !ok {
				return fmt.Errorf("initialization resume has a different stage kind")
			}
			return validatePrefix(stage, v.Done)
		case ResumePublish:
			if err := samePublish(v.Spec); err != nil {
				return err
			}
			if _, ok := stage.(PublishStage); !ok {
				return fmt.Errorf("publish resume has a different stage kind")
			}
			return validatePrefix(stage, v.Done)
		default:
			return fmt.Errorf("recovery state has no resume specification")
		}
		return nil
	}
	switch v := st.(type) {
	case Reserved:
		if err := sameInit(v.Spec); err != nil {
			return err
		}
		if v.Hash != spec.Hash() {
			return fmt.Errorf("reservation hash changed")
		}
	case AwaitingConfirm:
		if err := sameInit(v.Spec); err != nil {
			return err
		}
		if v.Hash != spec.Hash() || v.Proposal.Validate() != nil {
			return fmt.Errorf("invalid initialization confirmation")
		}
	case Initializing:
		if err := sameInit(v.Spec); err != nil {
			return err
		}
		if v.Attempt < 1 {
			return fmt.Errorf("initialization needs a positive attempt")
		}
		return validatePrefix(v.Stage, v.Done)
	case Ready:
		if v.Repo != spec.Repo || !validObject(v.InitialHead) {
			return fmt.Errorf("ready state has a different repo or invalid head")
		}
	case Publishing:
		if err := samePublish(v.Spec); err != nil {
			return err
		}
		if v.Attempt < 1 {
			return fmt.Errorf("publishing needs a positive attempt")
		}
		return validatePrefix(v.Stage, v.Done)
	case Published:
		if v.RemoteID == "" || !validObject(v.Head) || ValidateRemoteURL(v.URL) != nil {
			return fmt.Errorf("published state has invalid remote evidence")
		}
	case Uncertain:
		if v.Attempt < 1 {
			return fmt.Errorf("uncertain state needs a positive attempt")
		}
		return checkResume(v.Stage, v.Resume)
	case Stopped:
		if err := v.Reason.Validate(); err != nil {
			return err
		}
		return checkResume(v.Stage, v.Resume)
	default:
		return fmt.Errorf("unknown recovery state")
	}
	return nil
}

func validObject(s string) bool { return (len(s) == 40 || len(s) == 64) && isLowerHex(s) }

func receiptKind(r Receipt) string {
	switch r.(type) {
	case DirReceipt:
		return "dir"
	case GitReceipt:
		return "git"
	case FileReceipt:
		return "file"
	case CommitReceipt:
		return "commit"
	case TrackedReceipt:
		return "tracked"
	case RemoteReceipt:
		return "remote"
	case OriginReceipt:
		return "origin"
	case PushReceipt:
		return "push"
	}
	return ""
}

// ValidateStateShape validates an operation before storage, before the separate
// reservation binding check has loaded the immutable row.
func ValidateStateShape(st State) error {
	switch v := st.(type) {
	case Reserved:
		return ValidateState(v.Spec, st)
	case AwaitingConfirm:
		return ValidateState(v.Spec, st)
	case Initializing:
		return ValidateState(v.Spec, st)
	case Publishing:
		if err := v.Spec.Validate(); err != nil {
			return err
		}
		if v.Attempt < 1 {
			return fmt.Errorf("publishing needs a positive attempt")
		}
		return validatePrefix(v.Stage, v.Done)
	case Ready:
		if v.Repo.Validate() != nil || !validObject(v.InitialHead) {
			return fmt.Errorf("invalid ready operation")
		}
	case Published:
		if v.RemoteID == "" || !validObject(v.Head) || ValidateRemoteURL(v.URL) != nil {
			return fmt.Errorf("invalid published operation")
		}
	case Uncertain:
		if v.Attempt < 1 {
			return fmt.Errorf("uncertain operation needs a positive attempt")
		}
		return validateResumeShape(v.Stage, v.Resume)
	case Stopped:
		if err := v.Reason.Validate(); err != nil {
			return err
		}
		return validateResumeShape(v.Stage, v.Resume)
	default:
		return fmt.Errorf("unknown operation state")
	}
	return nil
}

func validateResumeShape(stage Stage, resume Resumable) error {
	switch v := resume.(type) {
	case ResumeNone:
		if stage != nil {
			return fmt.Errorf("a staged operation needs resume evidence")
		}
		return nil
	case ResumeInit:
		if err := v.Spec.Validate(); err != nil {
			return err
		}
		if _, ok := stage.(InitStage); !ok {
			return fmt.Errorf("resume stage is not initialization")
		}
		return validatePrefix(stage, v.Done)
	case ResumePublish:
		if err := v.Spec.Validate(); err != nil {
			return err
		}
		if _, ok := stage.(PublishStage); !ok {
			return fmt.Errorf("resume stage is not publishing")
		}
		return validatePrefix(stage, v.Done)
	}
	return fmt.Errorf("operation needs resume evidence")
}
