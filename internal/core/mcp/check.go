package mcp

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Argument limits. The proposals core re-checks every propose_* value; these
// are the same rules, applied early so an agent gets a plain message at once.
const (
	DefaultListLimit = 20
	MaxPaths         = 200
	MaxMessageBytes  = 4 << 10
	MaxTitleLen      = 200
	MaxSummaryLen    = 1 << 10
	MaxContentBytes  = 256 << 10
	maxNameLen       = 128
	maxRefLen        = 255
	maxRemoteLen     = 100

	// The Phase 6 proposal limits, equal to core/proposals (TestDrift* keeps
	// them in step).
	MaxIgnoreEntries = 32
	MaxIgnoreBytes   = 4 << 10
	MaxCommentBytes  = 16 << 10
	maxAccountLen    = 100
	maxHostLen       = 255
	// MaxMRNumber keeps a merge request number well inside every host's range.
	MaxMRNumber = 1 << 30

	VisibilityPrivate = "private"
	VisibilityPublic  = "public"
)

func bad(format string, a ...any) error { return fmt.Errorf(format, a...) }

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// checkName accepts a repo name or ID, a preset or a dev server name: one
// short line of plain text with no separator, so it can never be a path.
func checkName(what, s string) error {
	switch {
	case s == "":
		return bad("%s is required", what)
	case len(s) > maxNameLen:
		return bad("%s is over %d characters", what, maxNameLen)
	case !utf8.ValidString(s) || hasControl(s):
		return bad("%s has characters that are not allowed", what)
	case strings.ContainsAny(s, `/\:`) || s == "." || s == "..":
		return bad("%s must be a name, not a path", what)
	}
	return nil
}

func checkRepo(r RepoRef) error { return checkName("repo", string(r)) }

func checkStepName(s string) error {
	if s == "" || len(s) > 64 {
		return bad("a build step name must be 1 to 64 characters")
	}
	for _, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == ':'
		if !ok {
			return bad("build step %q may only use letters, digits, dot, dash, colon and underscore", s)
		}
	}
	return nil
}

func windowsDevice(seg string) bool {
	stem := seg
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		return stem[3] >= '0' && stem[3] <= '9'
	}
	return false
}

func shortAlias(seg string) bool {
	for i := 0; i+1 < len(seg); i++ {
		if seg[i] == '~' && seg[i+1] >= '0' && seg[i+1] <= '9' {
			return true
		}
	}
	return false
}

// checkRelPath: relative, forward slashes, inside the root, no drive, stream,
// device name, control character or ".git" part (docs/design/proposals.md).
func checkRelPath(p string) error {
	switch {
	case p == "":
		return bad("a path is empty")
	case !utf8.ValidString(p) || hasControl(p):
		return bad("path %q has characters that are not allowed", p)
	case strings.Contains(p, `\`):
		return bad("path %q uses a backslash; write paths with forward slashes", p)
	case strings.HasPrefix(p, "/"):
		return bad("path %q must be relative to the repo", p)
	case strings.Contains(p, ":"):
		return bad("path %q has a drive or stream marker (:); path must be relative", p)
	case strings.ContainsAny(p, `<>"|?*`):
		return bad("path %q has characters Windows does not allow", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return bad("path %q has an empty part", p)
		case seg == "." || seg == "..":
			return bad("path %q must stay inside the repo (%s)", p, seg)
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return bad("path %q has a part ending in a dot or space", p)
		case shortAlias(seg):
			return bad("path %q has a part like NAME~1, a Windows short name", p)
		case strings.EqualFold(seg, ".git"):
			return bad("path %q is inside .git", p)
		case windowsDevice(seg):
			return bad("path %q uses a reserved Windows device name", p)
		}
	}
	return nil
}

func checkPaths(ps []string) error {
	if len(ps) == 0 {
		return bad("no paths given")
	}
	if len(ps) > MaxPaths {
		return bad("%d paths given; at most %d are allowed", len(ps), MaxPaths)
	}
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if err := checkRelPath(p); err != nil {
			return err
		}
		k := strings.ToLower(p)
		if seen[k] {
			return bad("path %q is listed twice", p)
		}
		seen[k] = true
	}
	return nil
}

func checkBranch(b string) error {
	switch {
	case b == "" || len(b) > maxRefLen:
		return bad("branch name must be 1 to %d characters", maxRefLen)
	case !utf8.ValidString(b) || hasControl(b) || strings.ContainsAny(b, " ~^:?*[\\"):
		return bad("branch name %q has characters git does not allow", b)
	case b == "@" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "+") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, "."):
		return bad("branch name %q is not a valid git branch name", b)
	case strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{"):
		return bad("branch name %q is not a valid git branch name", b)
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return bad("branch name %q is not a valid git branch name", b)
		}
	}
	return nil
}

func checkRemote(r string) error {
	if r == "" || len(r) > maxRemoteLen || strings.HasPrefix(r, "-") {
		return bad("remote name %q is not valid", r)
	}
	for _, c := range r {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return bad("remote name %q may only use letters, digits, dot, dash and underscore", r)
		}
	}
	return nil
}

func checkText(what, s string, max int, required bool) error {
	switch {
	case required && strings.TrimSpace(s) == "":
		return bad("%s is empty", what)
	case len(s) > max:
		return bad("%s is over %d bytes", what, max)
	case !utf8.ValidString(s) || strings.ContainsRune(s, 0):
		return bad("%s has characters that are not allowed", what)
	}
	return nil
}

func checkLine(what, s string, max int, required bool) error {
	if err := checkText(what, s, max, required); err != nil {
		return err
	}
	if strings.ContainsAny(s, "\r\n") || hasControl(s) {
		return bad("%s must be one line of plain text", what)
	}
	return nil
}

func checkLimit(n int) error {
	if n < 1 || n > MaxListLimit {
		return bad("limit must be between 1 and %d", MaxListLimit)
	}
	return nil
}

func checkMaxBytes(n, limit int) error {
	if n < 1 || n > limit {
		return bad("max_bytes must be between 1 and %d", limit)
	}
	return nil
}

// CheckCall reports whether c is a well-formed call: every rule ParseCall
// applies. Whatever ParseCall accepts passes CheckCall.
func CheckCall(c Call) error {
	if c == nil {
		return bad("no call")
	}
	switch v := c.(type) {
	case ListRepos, BuildOrder, ListBuildPresets, ListDevServers:
		return nil
	case RepoStatus:
		return checkRepo(v.Repo)
	case RemoteStatus:
		return checkRepo(v.Repo)
	case ChangedFiles:
		return checkRepo(v.Repo)
	case BuildInfo:
		return checkRepo(v.Repo)
	case Fetch:
		return checkRepo(v.Repo)
	case Pull:
		return checkRepo(v.Repo)
	case RecentCommits:
		if err := checkRepo(v.Repo); err != nil {
			return err
		}
		return checkLimit(v.Limit)
	case GetDiff:
		if err := checkRepo(v.Repo); err != nil {
			return err
		}
		if v.Path != "" {
			if err := checkRelPath(v.Path); err != nil {
				return err
			}
		}
		return checkMaxBytes(v.MaxBytes, MaxDiffBytes)
	case ListBuilds:
		return checkLimit(v.Limit)
	case BuildLog:
		if err := v.Build.Validate(); err != nil {
			return bad("build id is not valid")
		}
		if v.Step < 0 || v.Step >= MaxBuildSteps {
			return bad("step must be between 0 and %d", MaxBuildSteps-1)
		}
		return checkMaxBytes(v.MaxBytes, MaxLogBytes)
	case StartBuild:
		if len(v.Repos) == 0 || len(v.Repos) > MaxBuildSteps {
			return bad("name between 1 and %d repos to build", MaxBuildSteps)
		}
		for _, r := range v.Repos {
			if err := checkRepo(r); err != nil {
				return err
			}
		}
		if len(v.Steps) > MaxBuildSteps {
			return bad("%d build steps given; at most %d are allowed", len(v.Steps), MaxBuildSteps)
		}
		for _, s := range v.Steps {
			if err := checkStepName(s); err != nil {
				return err
			}
		}
		return nil
	case RunBuildPreset:
		return checkName("preset", v.Preset)
	case DevServerLog:
		return checkName("dev server name", v.Name)
	}
	return checkPropose(c)
}

// checkIgnoreEntry is the rule of core/proposals.CheckIgnoreEntry: an anchored
// folder such as /build/ with every pattern character escaped.
func checkIgnoreEntry(e string) error {
	if len(e) < 3 || len(e) > 512 || !utf8.ValidString(e) || hasControl(e) {
		return bad("an ignore entry is empty, too long or has control characters")
	}
	if e[0] != '/' || e[len(e)-1] != '/' {
		return bad("ignore entry %q must be an anchored folder such as /build/", e)
	}
	var plain strings.Builder
	esc := false
	for _, r := range e[1 : len(e)-1] {
		switch {
		case esc:
			plain.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case strings.ContainsRune("*?[]!#", r):
			return bad("ignore entry %q has an unescaped pattern character", e)
		default:
			plain.WriteRune(r)
		}
	}
	if esc {
		return bad("ignore entry %q ends in a lone backslash", e)
	}
	p := plain.String()
	if p == "" || strings.ContainsAny(p, `:<>"|`) {
		return bad("ignore entry %q has a character Windows does not allow", e)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") ||
			strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") || strings.HasPrefix(seg, " ") {
			return bad("ignore entry %q has an empty, relative or reserved part", e)
		}
	}
	return nil
}

func checkIgnoreEntries(es []string) error {
	if len(es) == 0 || len(es) > MaxIgnoreEntries {
		return bad("name between 1 and %d ignore entries", MaxIgnoreEntries)
	}
	seen := make(map[string]bool, len(es))
	total := 0
	for _, e := range es {
		if err := checkIgnoreEntry(e); err != nil {
			return err
		}
		if seen[e] {
			return bad("ignore entry %q is listed twice", e)
		}
		seen[e] = true
		total += len(e) + 1
	}
	if total > MaxIgnoreBytes {
		return bad("the ignore entries are over %d KiB", MaxIgnoreBytes>>10)
	}
	return nil
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// checkFullHead: a full lowercase hex commit ID (SHA-1 or SHA-256).
func checkFullHead(what, v string) error {
	if (len(v) != 40 && len(v) != 64) || !isLowerHex(v) {
		return bad("%s must be a full lowercase hex commit ID (40 or 64 digits)", what)
	}
	return nil
}

func checkCommentBody(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return bad("the comment is empty")
	case len(s) > MaxCommentBytes:
		return bad("the comment is over %d KiB", MaxCommentBytes>>10)
	case !utf8.ValidString(s):
		return bad("the comment is not valid text")
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			return bad("the comment has control characters")
		}
	}
	return nil
}

// checkAccount is the rule of core/proposals for a host account or repository
// name: letters, digits, dash, underscore and dot, not starting with a dot or
// dash and not ending in .git.
func checkAccount(what, v string) error {
	if v == "" || len(v) > maxAccountLen || v[0] == '.' || v[0] == '-' || strings.HasSuffix(v, ".git") {
		return bad("the %s is not valid", what)
	}
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return bad("the %s may only hold letters, digits, - _ and .", what)
		}
	}
	return nil
}

// checkHost: a lowercase host name with no space, slash, backslash or @.
func checkHost(v string) error {
	if v == "" || len(v) > maxHostLen || v != strings.ToLower(v) || !utf8.ValidString(v) ||
		strings.ContainsFunc(v, func(c rune) bool { return c <= ' ' || c == '/' || c == '\\' || c == '@' || c == 0x7f }) {
		return bad("the host is not valid; use its lowercase name such as git.example.com")
	}
	return nil
}

func checkPropose(c Call) error {
	var repo RepoRef
	var summary string
	switch v := c.(type) {
	case ProposeCommit:
		repo, summary = v.Repo, v.Summary
		if err := checkPaths(v.Paths); err != nil {
			return err
		}
		if err := checkText("the commit message", v.Message, MaxMessageBytes, true); err != nil {
			return err
		}
	case ProposePush:
		repo, summary = v.Repo, v.Summary
		if err := checkBranch(v.Branch); err != nil {
			return err
		}
		if err := checkRemote(v.Remote); err != nil {
			return err
		}
	case ProposeMergeRequest:
		repo, summary = v.Repo, v.Summary
		if err := checkBranch(v.Branch); err != nil {
			return err
		}
		if err := checkRemote(v.Remote); err != nil {
			return err
		}
		if err := checkBranch(v.Target); err != nil {
			return err
		}
		if err := checkLine("the merge request title", v.Title, MaxTitleLen, true); err != nil {
			return err
		}
		if err := checkLine("the merge request description", v.Description, MaxMessageBytes, false); err != nil {
			return err
		}
	case ProposeInstall:
		repo, summary = v.Repo, v.Summary
	case ProposeAgentFile:
		repo, summary = v.Repo, v.Summary
		if err := checkRelPath(v.Path); err != nil {
			return err
		}
		if err := checkText("the file content", v.Content, MaxContentBytes, false); err != nil {
			return err
		}
	case ProposeDeleteFiles:
		repo, summary = v.Repo, v.Summary
		if err := checkPaths(v.Paths); err != nil {
			return err
		}
	case ProposeIgnore:
		repo, summary = v.Repo, v.Summary
		if err := checkIgnoreEntries(v.Entries); err != nil {
			return err
		}
	case ProposeReviewComment:
		repo, summary = v.Repo, v.Summary
		if v.MR < 1 || v.MR > MaxMRNumber {
			return bad("mr must be a merge request number between 1 and %d", MaxMRNumber)
		}
		if err := checkFullHead("expected_head", v.ExpectedHead); err != nil {
			return err
		}
		if err := checkCommentBody(v.Body); err != nil {
			return err
		}
	case ProposePublish:
		repo, summary = v.Repo, v.Summary
		if err := checkHost(v.Host); err != nil {
			return err
		}
		if err := checkAccount("owner", v.Owner); err != nil {
			return err
		}
		if err := checkAccount("repository name", v.Name); err != nil {
			return err
		}
		if v.OwnerKind != "user" && v.OwnerKind != "org" {
			return bad("owner_kind must be user or org")
		}
		if v.Visibility != VisibilityPrivate && v.Visibility != VisibilityPublic {
			return bad("visibility must be private or public")
		}
	default:
		return bad("unknown call %T", c)
	}
	if err := checkRepo(repo); err != nil {
		return err
	}
	return checkLine("the summary", summary, MaxSummaryLen, false)
}
