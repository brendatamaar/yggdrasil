package standup

import (
	"slices"
	"strconv"
	"strings"
)

const (
	textFooter = "[Text limit reached: some entries are not listed.]"
	timeLayout = "2006-01-02 15:04"
)

// Format renders a summary deterministically: stable order by repo name, time
// and identity whatever the order of the slices, at most MaxTextBytes. It names
// no unavailable source (a Summary does not carry them); BuildSummary renders
// the text with them. It never invents content: an empty source says so.
func Format(s Summary) string {
	text, _ := render(s, nil)
	return text
}

type textLine struct {
	// kind is the source the line belongs to, or -1 for a line that is never
	// counted as cut (header, notes, empty-section lines).
	kind int
	text string
}

// render returns the text and the sources whose entries did not fit under
// MaxTextBytes. Lines are cut whole, never in the middle.
func render(s Summary, unavail []Unavailable) (string, []SourceKind) {
	commits := slices.Clone(s.Commits)
	mrs := slices.Clone(s.MRs)
	builds := slices.Clone(s.Builds)
	slices.SortFunc(commits, cmpCommit)
	slices.SortFunc(mrs, cmpMR)
	slices.SortFunc(builds, cmpBuild)

	whole := map[SourceKind]bool{}
	labelled := map[SourceKind]bool{}
	for _, u := range unavail {
		labelled[u.Source] = true
		if u.Repo == "" {
			whole[u.Source] = true
		}
	}

	var lines []textLine
	add := func(kind int, format string) { lines = append(lines, textLine{kind, format}) }

	add(-1, "Standup for project "+cleanText(string(s.Range.Project), maxNameBytes)+", "+
		s.Range.From.UTC().Format(timeLayout)+" to "+s.Range.To.UTC().Format(timeLayout)+" UTC (end excluded)")

	if len(unavail) > 0 {
		add(-1, "")
		add(-1, "Not available:")
		for _, u := range unavail {
			what := sourceTitle(u.Source)
			if u.Repo != "" {
				name := u.RepoName
				if name == "" {
					name = string(u.Repo)
				}
				add(-1, "- "+what+" for "+name+": "+u.Reason)
			} else {
				add(-1, "- "+what+": "+u.Reason)
			}
		}
	}

	section := func(k SourceKind, n int, items func(emit func(string))) {
		add(-1, "")
		title := sourceTitle(k)
		switch {
		case whole[k]:
			add(-1, title+": not read.")
			return
		case n == 0 && labelled[k]:
			add(-1, title+": none found in the repos that could be read.")
			return
		case n == 0:
			add(-1, title+": none in this range.")
			return
		}
		add(kindIndex(k), title+" ("+strconv.Itoa(n)+")")
		items(func(t string) { add(kindIndex(k), t) })
		if slices.Contains(s.Truncated, k) {
			add(-1, "Only "+strconv.Itoa(n)+" are listed; there are more.")
		}
	}
	section(SourceCommits, len(commits), func(emit func(string)) {
		for _, c := range commits {
			emit("- " + repoLabel(c.RepoName, string(c.Repo)) + " " + shortOID(c.OID) + " " + c.Subject + " (" + c.Author + ", " + c.At.UTC().Format(timeLayout) + "Z)")
		}
	})
	section(SourceMRs, len(mrs), func(emit func(string)) {
		for _, m := range mrs {
			emit("- " + repoLabel(m.RepoName, string(m.Repo)) + " !" + strconv.Itoa(m.Number) + " " + m.Title + " [" + m.State + "] (" + m.At.UTC().Format(timeLayout) + "Z)")
		}
	})
	section(SourceBuilds, len(builds), func(emit func(string)) {
		for _, b := range builds {
			emit("- " + repoLabel(b.RepoName, string(b.Repo)) + " build " + string(b.ID) + " " + string(b.Status) + " (" + b.At.UTC().Format(timeLayout) + "Z)")
		}
	})

	limit := MaxTextBytes - len(textFooter) - 1
	var b strings.Builder
	stopped := false
	cut := map[int]bool{}
	for _, l := range lines {
		if !stopped && b.Len()+len(l.text)+1 > limit {
			stopped = true
		}
		if stopped {
			if l.kind >= 0 {
				cut[l.kind] = true
			}
			continue
		}
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	if stopped {
		b.WriteString(textFooter)
	} else {
		return strings.TrimRight(b.String(), "\n"), nil
	}
	var cutKinds []SourceKind
	for i, k := range sourceOrder {
		if cut[i] {
			cutKinds = append(cutKinds, k)
		}
	}
	return b.String(), cutKinds
}

func sourceTitle(k SourceKind) string {
	switch k {
	case SourceCommits:
		return "Commits"
	case SourceMRs:
		return "Merge requests"
	case SourceBuilds:
		return "Builds"
	}
	return string(k)
}

func repoLabel(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func shortOID(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}
