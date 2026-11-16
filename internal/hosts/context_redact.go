package hosts

import (
	"encoding/json"
	"strings"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func cloneContext(p corehosts.ContextPayload) corehosts.ContextPayload {
	switch v := p.(type) {
	case corehosts.JobLogs:
		v.Jobs = append([]corehosts.JobLog(nil), v.Jobs...)
		return v
	case corehosts.IssueList:
		v.Items = append([]corehosts.IssueSummary(nil), v.Items...)
		for i := range v.Items {
			v.Items[i].Labels = append([]string(nil), v.Items[i].Labels...)
		}
		return v
	case corehosts.IssueDetail:
		v.Summary.Labels = append([]string(nil), v.Summary.Labels...)
		return v
	case corehosts.MRContext:
		return v
	}
	return nil
}

func redactContext(r ContextResult, tokens ...string) ContextResult {
	red := safety.NewRedactor(tokens...)
	remaining := corehosts.ContextMaxPromptBytes
	truncated := false
	text := func(s string) string {
		// Strip invalid bytes first: removing them after redaction could join the
		// two halves of a credential that the redactor could not see as one.
		s = strings.ToValidUTF8(red.Redact(strings.ToValidUTF8(s, "")), "")
		if len(s) > remaining {
			truncated = true
			s = strings.ToValidUTF8(s[:remaining], "")
		}
		remaining -= len(s)
		return s
	}
	r.Unavailable = append([]corehosts.ContextUnavailable(nil), r.Unavailable...)
	for i := range r.Unavailable {
		r.Unavailable[i].Source = text(r.Unavailable[i].Source)
	}
	summary := func(v corehosts.IssueSummary) corehosts.IssueSummary {
		v.Title = text(v.Title)
		v.Author = text(v.Author)
		v.Labels = append([]string(nil), v.Labels...)
		for i := range v.Labels {
			v.Labels[i] = text(v.Labels[i])
		}
		return v
	}
	switch v := cloneContext(r.Value).(type) {
	case corehosts.JobLogs:
		for i := range v.Jobs {
			j := &v.Jobs[i]
			j.Name = text(j.Name)
			j.Stage = text(j.Stage)
			before := len(j.Log)
			j.Log = text(j.Log)
			j.LogTruncated = j.LogTruncated || len(j.Log) < before
			j.LogUnavailable = text(j.LogUnavailable)
		}
		r.Value = v
	case corehosts.IssueList:
		for i := range v.Items {
			v.Items[i] = summary(v.Items[i])
		}
		r.Value = v
	case corehosts.IssueDetail:
		v.Summary = summary(v.Summary)
		beforeURL := v.URL
		v.URL = text(v.URL)
		if v.URL != beforeURL {
			v.URL = ""
		}
		before := len(v.Body)
		v.Body = text(v.Body)
		v.BodyTruncated = v.BodyTruncated || len(v.Body) < before
		r.Value = v
	case corehosts.MRContext:
		v.MR.Title = text(v.MR.Title)
		v.MR.Author = text(v.MR.Author)
		v.MR.Source = text(v.MR.Source)
		v.MR.Target = text(v.MR.Target)
		beforeURL := v.MR.URL
		v.MR.URL = text(v.MR.URL)
		if v.MR.URL != beforeURL {
			v.MR.URL = ""
		}
		before := len(v.Diff)
		v.Diff = text(v.Diff)
		v.DiffTruncated = v.DiffTruncated || len(v.Diff) < before
		r.Value = v
	}
	if truncated {
		switch v := r.Value.(type) {
		case corehosts.JobLogs:
			v.Truncated = true
			r.Value = v
		case corehosts.IssueList:
			v.Truncated = true
			r.Value = v
		case corehosts.IssueDetail:
			v.BodyTruncated = true
			r.Value = v
		case corehosts.MRContext:
			v.DiffTruncated = true
			r.Value = v
		}
	}
	return boundContext(r)
}

// maxUnavailableSource is the longest label of a missing part that boundContext leaves whole.
const maxUnavailableSource = 256

// boundContext also bounds structural overhead (IDs, labels, empty strings and
// JSON field names), rather than counting only the visible body text.
func boundContext(r ContextResult) ContextResult {
	half := func(s string) string { return strings.ToValidUTF8(s[:len(s)/2], "") }
	summary := func(s corehosts.IssueSummary) corehosts.IssueSummary {
		s.Title = half(s.Title)
		s.Author = half(s.Author)
		s.Labels = s.Labels[:len(s.Labels)/2]
		return s
	}
	for pass := 0; pass < 32; pass++ {
		encoded, err := json.Marshal(r)
		if err != nil {
			return ContextResult{}
		}
		if len(encoded) <= corehosts.ContextMaxPromptBytes {
			return r
		}
		// Labels of missing parts are short; only an oversized one is cut, so a
		// body overflow never mangles "external checks" into "externa".
		for i := range r.Unavailable {
			if len(r.Unavailable[i].Source) > maxUnavailableSource {
				r.Unavailable[i].Source = half(r.Unavailable[i].Source)
			}
		}
		if len(r.Unavailable) > 1 {
			r.Unavailable = r.Unavailable[:len(r.Unavailable)/2]
			r.Unavailable[len(r.Unavailable)-1] = corehosts.ContextUnavailable{Source: "additional context omitted", Reason: corehosts.ContextTooLarge}
		}
		switch v := r.Value.(type) {
		case corehosts.JobLogs:
			v.Truncated = true
			if len(v.Jobs) > 1 {
				v.Jobs = v.Jobs[:len(v.Jobs)/2]
			} else if len(v.Jobs) == 1 {
				j := &v.Jobs[0]
				j.Log = half(j.Log)
				j.Name = half(j.Name)
				j.Stage = half(j.Stage)
				j.LogUnavailable = half(j.LogUnavailable)
				j.LogTruncated = true
			}
			r.Value = v
		case corehosts.IssueList:
			v.Truncated = true
			if len(v.Items) > 1 {
				v.Items = v.Items[:len(v.Items)/2]
			} else if len(v.Items) == 1 {
				v.Items[0] = summary(v.Items[0])
			}
			r.Value = v
		case corehosts.IssueDetail:
			v.BodyTruncated = true
			v.Body = half(v.Body)
			v.Summary = summary(v.Summary)
			v.URL = ""
			r.Value = v
		case corehosts.MRContext:
			v.DiffTruncated = true
			v.Diff = half(v.Diff)
			v.MR.Title = half(v.MR.Title)
			v.MR.Author = half(v.MR.Author)
			v.MR.Source = half(v.MR.Source)
			v.MR.Target = half(v.MR.Target)
			v.MR.URL = ""
			r.Value = v
		}
	}
	return ContextResult{}
}
