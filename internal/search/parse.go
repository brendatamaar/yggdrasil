package search

import (
	"errors"
	"fmt"
	"regexp/syntax"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
)

// ParseRequest is the boundary parser of POST /api/search. It applies the
// core's ParseQuery, then checks the shape of a regex pattern with Go's
// regexp/syntax (the core may not import it). The shape check is an
// approximation of git's ERE: Go rejects back-references such as \1 that
// GNU ERE accepts, and accepts a few Perl-only forms such as \d that GNU ERE
// reads as a plain letter; "(?" groups are rejected because ERE has none.
// A pattern that passes here and still fails in git ends as a git_failed
// problem for that repo, never as an unsafe call.
//
// The error is an apperr.Invalid with a plain-language message.
func ParseRequest(raw coresearch.RawQuery) (coresearch.Query, error) {
	q, err := coresearch.ParseQuery(raw)
	if err != nil {
		return coresearch.Query{}, invalid(err)
	}
	if err := CheckRegex(q); err != nil {
		return coresearch.Query{}, err
	}
	return q, nil
}

// CheckRegex rejects a regex-mode pattern that is not a valid expression.
// Literal mode always passes.
func CheckRegex(q coresearch.Query) error {
	if q.Mode != coresearch.ModeRegex {
		return nil
	}
	if strings.Contains(q.Pattern, "(?") {
		return apperr.New(apperr.Invalid, "The regular expression uses a \"(?\" group, which is not supported. Remove it or switch to plain text search.", nil)
	}
	if _, err := syntax.Parse(q.Pattern, syntax.Perl); err != nil {
		var se *syntax.Error
		why := "it is not a valid expression"
		if errors.As(err, &se) {
			why = fmt.Sprintf("%s near %q", se.Code, se.Expr)
		}
		return apperr.New(apperr.Invalid, "The regular expression is not valid ("+why+"). Fix it or switch to plain text search.", err)
	}
	return nil
}

func invalid(err error) error {
	msg := strings.TrimPrefix(err.Error(), coresearch.ErrInvalidQuery.Error()+": ")
	if msg == "" {
		msg = "the search is not valid"
	}
	return apperr.New(apperr.Invalid, strings.ToUpper(msg[:1])+msg[1:]+".", err)
}
