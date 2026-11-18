package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/core/standup"
)

// The boundary parsers of the Phase 6 endpoints (docs/AGENTS.md 5.11). Each
// one turns outside text into a validated value or a plain-language refusal;
// a handler answers 400 with that text. They hold no state and do no IO. This
// file is Go-only wiring and is excluded from tygo by having no exported names.

const (
	// maxPlainName bounds an agent name; maxModelName a model name.
	maxPlainName = actions.MaxAgentLen
	maxModelName = actions.MaxModelLen
	// maxRequestIDLen matches core/actions.
	maxRequestIDLen = 100
	// maxQueryValue bounds any query value we parse (a cursor, a timestamp).
	maxQueryValue = 64
	// maxNumberDigits bounds an issue or MR number.
	maxNumberDigits = 9
	// defaultNoticePage is the page size when the client sends no limit.
	defaultNoticePage = 50
)

// refusal6 is a boundary rejection: its text is shown to the person.

type refusal6 struct{ msg string }

func (e *refusal6) Error() string { return e.msg }

func reject6(msg string) error { return &refusal6{msg: msg} }

// parseRepoID reads a tracked repo ID from a path value.
func parseRepoID(v string) (core.RepoID, error) {
	id := core.RepoID(v)
	if id.Validate() != nil {
		return "", reject6("That repo ID isn't valid.")
	}
	return id, nil
}

func parseProjectID(v string) (core.ProjectID, error) {
	id := core.ProjectID(v)
	if id.Validate() != nil {
		return "", reject6("That project ID isn't valid.")
	}
	return id, nil
}

func parseTaskIDValue(v string) (core.TaskID, error) {
	id := core.TaskID(v)
	if id.Validate() != nil {
		return "", reject6("That task ID isn't valid.")
	}
	return id, nil
}

func parseOperationID(v string) (core.OperationID, error) {
	id := core.OperationID(v)
	if id.Validate() != nil {
		return "", reject6("That new repo ID isn't valid.")
	}
	return id, nil
}

func parseRequestID(v string) (core.RequestID, error) {
	id := core.RequestID(v)
	if id.Validate() != nil || len(v) > maxRequestIDLen {
		return "", reject6("The request ID isn't valid. Reload the page and try again.")
	}
	return id, nil
}

// parseNumber reads an issue or merge request number: plain decimal digits,
// at least 1, no sign, no spaces, no leading zero.
func parseNumber(v string) (int, error) {
	n, err := parseDigits(v, maxNumberDigits)
	if err != nil {
		return 0, reject6("That number isn't valid.")
	}
	return int(n), nil
}

// parseRunID reads a CI run ID: a positive decimal number, never a URL or path.
func parseRunID(v string) (string, error) {
	if _, err := parseDigits(v, 18); err != nil {
		return "", reject6("That run ID isn't valid.")
	}
	return v, nil
}

func parseDigits(v string, maxDigits int) (int64, error) {
	if v == "" || len(v) > maxDigits || v[0] == '0' {
		return 0, errors.New("bad number")
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, errors.New("bad number")
		}
	}
	return strconv.ParseInt(v, 10, 64)
}

// plainName checks an agent name: letters, digits, dot, dash, underscore.
func plainName(what, v string, required bool) error {
	if v == "" {
		if required {
			return reject6("Choose an agent.")
		}
		return nil
	}
	if len(v) > maxPlainName {
		return reject6("The " + what + " name is too long.")
	}
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			return reject6("The " + what + " name isn't valid.")
		}
	}
	return nil
}

func modelName(v string) error {
	if len(v) > maxModelName || !utf8.ValidString(v) || strings.ContainsFunc(v, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return reject6("The model name isn't valid.")
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

// checkHash accepts a sha256 hex digest.
func checkHash(what, v string) error {
	if len(v) != 64 || !isLowerHex(v) {
		return reject6("The " + what + " isn't valid. Preview again and retry.")
	}
	return nil
}

// checkHead accepts an empty value or a full lowercase commit ID.
func checkHead(v string) error {
	if v == "" {
		return nil
	}
	if n := len(v); (n != 40 && n != 64) || !isLowerHex(v) {
		return reject6("The expected head isn't a full commit ID. Preview again and retry.")
	}
	return nil
}

// trimInvalid removes the sentinel prefix a core parser puts in front of its
// plain sentence.
func trimInvalid(err error, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// parseActionRequest validates the identity part of a preview or execute.
func parseActionRequest(r ActionRequestDTO) error {
	if _, err := parseRequestID(string(r.RequestID)); err != nil {
		return err
	}
	if _, err := actions.ParseRequest(string(r.RequestID), string(r.RepoID), string(r.Kind), r.SourceID, r.Agent, r.Model); err != nil {
		return reject6("That action isn't valid: " + trimInvalid(err, actions.ErrInvalidRequest))
	}
	return nil
}

func parseActionExecute(r ActionExecuteRequest) error {
	if err := parseActionRequest(r.ActionRequestDTO); err != nil {
		return err
	}
	if err := checkHash("preview digest", r.Digest); err != nil {
		return err
	}
	return checkHead(r.ExpectedHead)
}

func parseImportDraft(req ImportIssueDraftRequest) error {
	if _, err := parseRequestID(string(req.RequestID)); err != nil {
		return err
	}
	if err := plainName("agent", req.Agent, true); err != nil {
		return err
	}
	return modelName(req.Model)
}

func parseStartDraft(req StartDraftRequest) error {
	if err := req.Mode.Validate(); err != nil {
		return reject6("Choose a mode for the task.")
	}
	return modelName(req.Model)
}

func parseIgnoreProposal(req CreateIgnoreProposalRequest) error {
	if _, err := parseRequestID(string(req.RequestID)); err != nil {
		return err
	}
	n := len(req.Entries)
	if n == 0 || n > coreprop.MaxIgnoreEntries {
		return reject6("Choose between 1 and " + strconv.Itoa(coreprop.MaxIgnoreEntries) + " entries to ignore.")
	}
	total := 0
	seen := make(map[string]struct{}, n)
	for _, e := range req.Entries {
		if err := coreprop.CheckIgnoreEntry(e); err != nil {
			return reject6("One of the entries can't be added to .gitignore: " + trimInvalid(err, coreprop.ErrInvalidAction))
		}
		if _, dup := seen[e]; dup {
			return reject6("The same entry was chosen twice.")
		}
		seen[e] = struct{}{}
		total += len(e)
	}
	if total > coreprop.MaxIgnoreBytes {
		return reject6("The chosen entries are too long together.")
	}
	return nil
}

// parseNoticeQuery reads ?after= and ?limit=.
func parseNoticeQuery(r *http.Request) (after int64, limit int, err error) {
	q := r.URL.Query()
	limit = defaultNoticePage
	if v := q.Get("after"); v != "" {
		if len(v) > 18 {
			return 0, 0, reject6("The 'after' value must be a whole number of 0 or more.")
		}
		for _, c := range v {
			if c < '0' || c > '9' {
				return 0, 0, reject6("The 'after' value must be a whole number of 0 or more.")
			}
		}
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, 0, reject6("The 'after' value must be a whole number of 0 or more.")
		}
	}
	if v := q.Get("limit"); v != "" {
		n, perr := parseDigits(v, 4)
		if perr != nil || n > notify.MaxPage {
			return 0, 0, reject6("The 'limit' value must be a whole number from 1 to " + strconv.Itoa(notify.MaxPage) + ".")
		}
		limit = int(n)
	}
	return after, limit, nil
}

// parseRangeQuery reads ?from= and ?to= (RFC 3339).
func parseRangeQuery(project core.ProjectID, r *http.Request) (standup.Range, error) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" || to == "" {
		return standup.Range{}, reject6("Say which period to summarize: send 'from' and 'to' as RFC 3339 times.")
	}
	if len(from) > maxQueryValue || len(to) > maxQueryValue {
		return standup.Range{}, reject6("The 'from' or 'to' time isn't valid.")
	}
	f, err1 := time.Parse(time.RFC3339, from)
	t, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil {
		return standup.Range{}, reject6("The 'from' and 'to' times must be RFC 3339, for example 2026-03-01T00:00:00Z.")
	}
	return parseRange(project, f, t)
}

func parseRange(project core.ProjectID, from, to time.Time) (standup.Range, error) {
	rg, err := standup.ParseRange(project, from, to)
	if err != nil {
		return standup.Range{}, reject6("That period isn't valid: " + trimInvalid(err, standup.ErrInvalidRange))
	}
	return rg, nil
}

func parsePolish(project core.ProjectID, req StandupPolishRequest) error {
	if _, err := parseRequestID(string(req.RequestID)); err != nil {
		return err
	}
	if _, err := parseRange(project, req.From, req.To); err != nil {
		return err
	}
	if err := plainName("agent", req.Agent, true); err != nil {
		return err
	}
	return modelName(req.Model)
}

func checkSpecText(req NewRepoPreviewRequest) error {
	if err := newrepos.CheckLeaf(req.Leaf); err != nil {
		return reject6("The folder name isn't valid: " + trimInvalid(err, newrepos.ErrInvalidSpec))
	}
	for _, c := range []string{req.README, req.Ignore} {
		if len(c) > newrepos.MaxContent || !utf8.ValidString(c) || strings.ContainsRune(c, 0) {
			return reject6("A file's content is too long or isn't text.")
		}
	}
	if len(req.CommitMessage) > newrepos.MaxMessage || !utf8.ValidString(req.CommitMessage) || strings.ContainsRune(req.CommitMessage, 0) {
		return reject6("The commit message is too long or isn't text.")
	}
	return nil
}

func parseNewRepoPreview(req NewRepoPreviewRequest) error { return checkSpecText(req) }

func parseReserve(req ReserveNewRepoRequest) error {
	if _, err := parseRequestID(string(req.RequestID)); err != nil {
		return err
	}
	if err := checkSpecText(req.NewRepoPreviewRequest); err != nil {
		return err
	}
	if err := checkHash("preview hash", req.Hash); err != nil {
		return err
	}
	return plainName("agent", req.Agent, false)
}

func accountName(what, v string, max int) error {
	if v == "" || len(v) > max || v[0] == '.' || v[0] == '-' || strings.HasSuffix(v, ".git") {
		return reject6("The " + what + " is empty, too long or starts or ends with a reserved character.")
	}
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return reject6("The " + what + " may only hold letters, digits, - _ and .")
		}
	}
	return nil
}

func parsePublishPreview(req PublishPreviewRequest) error {
	if req.HostInstance == "" || len(req.HostInstance) > 100 || req.HostInstance != strings.ToLower(req.HostInstance) ||
		strings.ContainsFunc(req.HostInstance, func(c rune) bool { return c <= ' ' || c == '/' || c == '\\' || c == '@' || c == ':' || c == 0x7f }) {
		return reject6("The host isn't valid.")
	}
	if err := accountName("owner", req.Owner, newrepos.MaxOwner); err != nil {
		return err
	}
	if err := accountName("repository name", req.Name, newrepos.MaxRepoName); err != nil {
		return err
	}
	if newrepos.OwnerKind(req.OwnerKind).Validate() != nil {
		return reject6("Say whether the owner is a user or an organization.")
	}
	if req.Visibility != "" && newrepos.Visibility(req.Visibility).Validate() != nil {
		return reject6("Choose private or public.")
	}
	return nil
}

func parsePublish(req PublishNewRepoRequest) error {
	if _, err := parseRequestID(string(req.RequestID)); err != nil {
		return err
	}
	if err := parsePublishPreview(req.PublishPreviewRequest); err != nil {
		return err
	}
	return checkHash("preview hash", req.Hash)
}

func parseRetry(req RetryNewRepoRequest) error { return checkHash("confirmed hash", req.Hash) }
