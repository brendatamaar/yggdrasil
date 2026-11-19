package newrepos

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Limits of the boundary.
const (
	// MaxContent bounds the README and the ignore file.
	MaxContent = 64 << 10
	// MaxLeaf bounds the new folder name.
	MaxLeaf = 64
	// MaxMessage bounds the first commit message.
	MaxMessage = 4 << 10
	// MaxOwner and MaxRepoName bound the host account and repository names.
	MaxOwner    = 100
	MaxRepoName = 100
	// DefaultBranch is the initial branch of a new repo.
	DefaultBranch = "main"
)

// ErrInvalidSpec wraps every rejection of a spec.
var ErrInvalidSpec = errors.New("invalid new repo")

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(f, a...))
}

// InitStage is one local step of initialization. It is a closed, ordered set;
// what a finished stage left behind is a Receipt, not a bundle of booleans.
type InitStage string

const (
	StageCreateDirectory  InitStage = "create_directory"
	StageInitGit          InitStage = "init_git"
	StageWriteREADME      InitStage = "write_readme"
	StageWriteIgnore      InitStage = "write_ignore"
	StageFirstCommit      InitStage = "first_commit"
	StageFinalizeTracking InitStage = "finalize_tracking"
)

// InitStages lists the stages in execution order.
func InitStages() []InitStage {
	return []InitStage{StageCreateDirectory, StageInitGit, StageWriteREADME, StageWriteIgnore,
		StageFirstCommit, StageFinalizeTracking}
}

// PublishStage is one step of publishing.
type PublishStage string

const (
	StageCreateRemote PublishStage = "create_remote"
	StageAddOrigin    PublishStage = "add_origin"
	StagePushHead     PublishStage = "push_head"
)

// PublishStages lists the stages in execution order.
func PublishStages() []PublishStage {
	return []PublishStage{StageCreateRemote, StageAddOrigin, StagePushHead}
}

// Stage is either an InitStage or a PublishStage (sealed).
//
//sumtype:decl
type Stage interface {
	// Name is the stored stage name; the sets of names are disjoint.
	Name() string
	isStage()
}

func (s InitStage) Name() string    { return string(s) }
func (s PublishStage) Name() string { return string(s) }
func (InitStage) isStage()          {}
func (PublishStage) isStage()       {}

// Validate reports whether s is a known stage.
func (s InitStage) Validate() error {
	for _, x := range InitStages() {
		if s == x {
			return nil
		}
	}
	return fmt.Errorf("unknown init stage %q", string(s))
}

// Validate reports whether s is a known stage.
func (s PublishStage) Validate() error {
	for _, x := range PublishStages() {
		if s == x {
			return nil
		}
	}
	return fmt.Errorf("unknown publish stage %q", string(s))
}

// ParseStage turns a stored stage name back into a Stage.
func ParseStage(name string) (Stage, error) {
	if s := InitStage(name); s.Validate() == nil {
		return s, nil
	}
	if s := PublishStage(name); s.Validate() == nil {
		return s, nil
	}
	return nil, fmt.Errorf("unknown stage %q", name)
}

// Visibility of a published repo. Private is the default and the only value
// the UI preselects; public must be chosen and shows in the confirmation.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

// Validate reports whether v is known.
func (v Visibility) Validate() error {
	switch v {
	case VisibilityPrivate, VisibilityPublic:
		return nil
	}
	return fmt.Errorf("unknown visibility %q", string(v))
}

// OwnerKind says whether the owner on the host is a user or an organization.
// The server validates it against the host; the browser only chooses.
type OwnerKind string

const (
	OwnerUser OwnerKind = "user"
	OwnerOrg  OwnerKind = "org"
)

// Validate reports whether k is known.
func (k OwnerKind) Validate() error {
	switch k {
	case OwnerUser, OwnerOrg:
		return nil
	}
	return fmt.Errorf("unknown owner kind %q", string(k))
}

// InitSpec is what the user previews and confirms to initialize a reserved
// repo. It is immutable and hash-bound: a changed field is a new spec and a
// new proposal. Leaf is exactly one child folder name of the tracked project,
// never a path from the browser.
type InitSpec struct {
	Project       core.ProjectID
	Repo          core.RepoID
	Leaf          string
	Branch        string
	README        string
	Ignore        string
	CommitMessage string
}

// ParseInitSpec validates every field with the same rules as Validate.
func ParseInitSpec(s InitSpec) (InitSpec, error) {
	if err := s.Validate(); err != nil {
		return InitSpec{}, err
	}
	return s, nil
}

// Validate checks the spec: a valid leaf, the initial branch main, content
// within bounds, a nonempty single-purpose commit message.
func (s InitSpec) Validate() error {
	if s.Project.Validate() != nil || s.Repo.Validate() != nil {
		return invalid("the project or repo id is not valid")
	}
	if err := CheckLeaf(s.Leaf); err != nil {
		return err
	}
	if s.Branch != DefaultBranch {
		return invalid("the first branch of a new repo is %s", DefaultBranch)
	}
	for _, c := range []string{s.README, s.Ignore} {
		if len(c) > MaxContent || !utf8.ValidString(c) || strings.ContainsRune(c, 0) {
			return invalid("a file's content is over %d KiB or is not text", MaxContent>>10)
		}
	}
	m := strings.TrimSpace(s.CommitMessage)
	if m == "" || len(s.CommitMessage) > MaxMessage || !utf8.ValidString(s.CommitMessage) || strings.ContainsRune(s.CommitMessage, 0) {
		return invalid("the commit message is empty or too long")
	}
	return nil
}

// Hash is what the user confirms: sha256 hex over the length-prefixed fields.
func (s InitSpec) Hash() string {
	return hashFields("ygg-init-v1", string(s.Project), string(s.Repo), s.Leaf, s.Branch, s.README, s.Ignore, s.CommitMessage)
}

// PublishSpec is what the user previews and confirms to publish a ready repo
// to a host account. Host and Owner are resolved by the server; Marker is a
// random, non-secret operation marker the shell generated and which goes into
// the previewed repo description so a lost create reply can be reconciled.
type PublishSpec struct {
	Repo         core.RepoID
	HostInstance string
	Owner        string
	OwnerKind    OwnerKind
	Name         string
	Visibility   Visibility
	Branch       string
	ExpectedHead string
	Marker       string
}

// ParsePublishSpec validates every field with the same rules as Validate.
func ParsePublishSpec(s PublishSpec) (PublishSpec, error) {
	if err := s.Validate(); err != nil {
		return PublishSpec{}, err
	}
	return s, nil
}

// Validate checks the spec. Private is not forced here (public is a legal,
// explicit choice) but the zero value is rejected, so a caller cannot publish
// without choosing; the API preselects private.
func (s PublishSpec) Validate() error {
	if s.Repo.Validate() != nil {
		return invalid("the repo id is not valid")
	}
	if core.ValidateHostInstance(s.HostInstance) != nil {
		return invalid("the host is not valid")
	}
	if err := checkAccountName("owner", s.Owner, MaxOwner); err != nil {
		return err
	}
	if err := checkAccountName("repository name", s.Name, MaxRepoName); err != nil {
		return err
	}
	if s.OwnerKind.Validate() != nil {
		return invalid("say whether the owner is a user or an organization")
	}
	if s.Visibility.Validate() != nil {
		return invalid("choose private or public")
	}
	if s.Branch != DefaultBranch {
		return invalid("only the %s branch is published", DefaultBranch)
	}
	if n := len(s.ExpectedHead); (n != 40 && n != 64) || !isLowerHex(s.ExpectedHead) {
		return invalid("the expected head must be a full lowercase commit id")
	}
	if len(s.Marker) < 16 || len(s.Marker) > 64 || !isLowerHex(s.Marker) {
		return invalid("the operation marker is not valid")
	}
	return nil
}

// Hash is what the user confirms.
func (s PublishSpec) Hash() string {
	return hashFields("ygg-publish-v1", string(s.Repo), s.HostInstance, s.Owner, string(s.OwnerKind), s.Name, string(s.Visibility), s.Branch, s.ExpectedHead, s.Marker)
}

// Description is the previewed repository description: a fixed phrase and the
// marker. It carries no path, token or user text.
func (s PublishSpec) Description() string { return "Created by Yggdrasil [ygg:" + s.Marker + "]" }

func hashFields(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func checkAccountName(what, v string, max int) error {
	if v == "" || len(v) > max || v[0] == '.' || v[0] == '-' || strings.HasSuffix(v, ".git") {
		return invalid("the %s is empty, too long or starts or ends with a reserved character", what)
	}
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return invalid("the %s may only hold letters, digits, - _ and .", what)
		}
	}
	return nil
}

// CheckLeaf validates the name of the one new child folder. It rejects the
// empty name, "." and "..", any separator, drive or stream marker (":"),
// characters Windows does not allow, control characters, a trailing dot or
// space (Windows rewrites them), a name like NAME~1 (a Windows short-name
// alias), ".git" in any case, a reserved device name and an over-long name.
// Whether the folder exists is the shell's check, immediately before the write.
func CheckLeaf(s string) error {
	switch {
	case s == "" || s == "." || s == "..":
		return invalid("give the new folder a name")
	case len(s) > MaxLeaf || !utf8.ValidString(s):
		return invalid("the folder name is too long or not valid text")
	case strings.ContainsAny(s, `/\:<>"|?*`):
		return invalid("the folder name has a separator or a character Windows does not allow")
	case strings.ContainsFunc(s, func(c rune) bool { return c < 0x20 || c == 0x7f }):
		return invalid("the folder name has control characters")
	case strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") || strings.HasPrefix(s, " "):
		return invalid("the folder name starts with a space or ends in a dot or space")
	case strings.EqualFold(s, ".git") || hasShortAlias(s) || isDevice(s):
		return invalid("the folder name is reserved")
	}
	return nil
}

func hasShortAlias(seg string) bool {
	i := strings.LastIndexByte(seg, '~')
	if i < 0 || i == len(seg)-1 {
		return false
	}
	rest := seg[i+1:]
	if d := strings.IndexByte(rest, '.'); d >= 0 {
		rest = rest[:d]
	}
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isDevice(seg string) bool {
	stem := seg
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		rest := stem[3:]
		// COM0-COM9 and LPT0-LPT9, and the superscript digits Windows also reserves.
		switch rest {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
			return true
		}
	}
	return false
}
