package repos

// PullCompletion derives the outcome of a successful ff-only pull from HEAD
// observations supplied by the shell. afterRead says whether the post-pull
// status read succeeded. A failed read does not undo a successful pull, and
// must not turn it into a failure or claim it was up to date.
func PullCompletion(before, after string, afterRead bool) ActOutcome {
	if afterRead && after == before {
		return UpToDate{}
	}
	out := Pulled{From: shortOID(before)}
	if afterRead {
		out.To = shortOID(after)
	}
	return out
}

func shortOID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
