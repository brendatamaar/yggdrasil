# Code search — Model Functional Effect design

Covers XR-01: `git grep` across tracked repos, with links that open VS Code, Cursor or IntelliJ.

This is a request/response feature with one trivial state per request: results are merged as each repo answers. There is no persistence and no audit, because it reads only.

- Core: `internal/core/search` (merge and caps, editor links).
- Shell: `internal/search`.
- API: `internal/api/search.go`.

## State map

```go
type Query struct {
    Pattern  string
    Mode     Mode            // literal (default) | regex (git grep -E)
    Case     bool            // case-sensitive
    Repos    []core.RepoID   // empty = every active repo in scope
    Paths    []string        // optional pathspec globs, e.g. "*.java"
}
type Search struct {
    Pending  map[core.RepoID]bool
    Results  []Match         // ordered by (repo name, path, line)
    PerRepo  map[core.RepoID]int
    Problems map[core.RepoID]ProblemKind   // timed_out | git_failed | missing
    Truncated bool           // a cap was hit
}
type Match struct {
    Repo core.RepoID; Path string; Line, Col int; Text string // Text trimmed to 300 runes
}
```

Request state:

- `Searching`: `Pending` is not empty.
- `Done`: `Pending` is empty.

A search can also be cancelled when the client goes away: the request context ends and the workers stop. Nothing is left over, because there is no stored state.

Caps:

- 200 matches per repo (`git grep --max-count` per file, plus a cut in the merge);
- 1,000 matches in total;
- 300 runes per line;
- an overall deadline of 15 s.

## Inputs

| Input | From | Fields |
|---|---|---|
| `Begin` | HTTP request | `Query, Repos []RepoRef{ID, Name, Path}` |
| `RepoResult` | worker | `Repo, Matches []Match, Truncated bool` |
| `RepoFailed` | worker | `Repo, Kind ProblemKind` |
| `Deadline` | shell timer | — |

## Transition table

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| — | Begin | at least one repo | Searching, with every repo pending | `Grep{repo, query}` per repo (the shell bounds concurrency to 8) |
| — | Begin | no repos in scope | Done (empty) | — |
| Searching | RepoResult | repo pending | repo no longer pending; merge its matches up to the caps; `Truncated` if a cap was hit or the worker reported it | — |
| Searching | RepoFailed | repo pending | repo no longer pending; record the problem | — |
| Searching | Deadline | — | every pending repo → `Problems[timed_out]`; Done | `CancelAll` |
| any | RepoResult / RepoFailed | repo not pending | no change | none |

**Any other (state, input) pair: no change, no commands.**

## Invariants

| # | Invariant | Property test |
|---|---|---|
| C1 | `len(Results) ≤ 1000`, and for each repo `PerRepo ≤ 200`. | `TestPropCapsHold` |
| C2 | `Results` are sorted by (repo name, path, line, column), with no duplicates. | `TestPropResultsOrdered` |
| C3 | `Truncated` ⇔ some cap cut a match, or a worker said it truncated. | `TestPropTruncatedHonest` |
| C4 | Merging results in any arrival order gives the same final `Results` (order independence). | `TestPropMergeOrderIndependent` |
| C5 | After Done, every repo of `Begin` is in exactly one of: has matches, has no matches, `Problems`. | `TestPropEveryRepoAccounted` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `Grep{Repo, Path, Query}` | one per repo; the output is a stream parsed incrementally, and the worker stops reading at 200 matches | yes (read-only) | `RepoResult` or `RepoFailed` |
| `CancelAll` | one | yes | — (ctx cancel kills the git processes) |

## Call graph

```go
// Production
POST /api/search {pattern, mode, case, repos?, paths?}
  → search.ParseQuery (boundary)
  → repos := store.ListActiveRepos (filtered by the request)        // SF-02: only tracked repos
  → SearchService.Search(ctx, query, repos)
      ctx, cancel := context.WithTimeout(r.Context(), 15 s)        // client disconnect cancels too
      state := search.Begin(query, repos) [core]
      workers (errgroup, limit 8):
          git grep -n --column -I --null --no-color [-F|-E] [-i] --max-count 200 -e <pattern> -- [pathspecs]
              (internal/git.Grep; runs through platform.Start, never a shell)
          → search.ParseGrep (boundary, streaming, stops at the cap) → RepoResult / RepoFailed
          → state = search.Merge(state, input) [core] (on the request goroutine, from a results channel)
      deadline → search.Expire(state) [core]
  → 200 {matches[], problems{}, truncated} with an editor link per match

Editor links (pure, core): search.EditorURL(editor, absPath, line, col)
  vscode://file/<path>:<line>:<col> · cursor://file/<path>:<line>:<col> · idea://open?file=<path>&line=<line>
  (opened by the browser; no server process. A server-side "open" goes through WS-08.)
```

```go
// Tests
search.Run(begin, inputs) (core) → C1–C5
ParseGrep: recorded `git grep --null` output fixtures + fuzz
service: real git grep on t.TempDir repos (git required in CI) and a fake Grepper for timeouts
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| ParseQuery | empty pattern, pattern over 500 bytes, invalid regex (pre-checked with Go `regexp/syntax` for ERE shape), path glob starting with `-` or `:` (pathspec magic) | domain outcome | 400 with the rule |
| git grep | exit 1 (no matches) | domain outcome | `RepoResult{}` (empty) |
| git grep | exit ≥ 2 | domain outcome | `RepoFailed{git_failed}`; message from `git.UserMessage` |
| git grep | timeout or deadline | domain outcome | `Problems[timed_out]` |
| repo folder | missing | domain outcome | `RepoFailed{missing}` |
| output | unparseable line | escape hatch | line skipped and counted; logged at debug level |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `Grepper` (`Grep(ctx, path, Query, limit) (<-chan Match, func() error)`) | `internal/git.Grep` | scripted fake | `GrepperContract`: literal hit; regex hit; case-insensitive; pathspec filter; binary files skipped (`-I`); no match → empty; a cap stops early; ctx cancel stops the process. Runs against the fake and against real git on temp repos |
| `ActiveRepos` | `internal/db` | SQLite temp | existing |

## Persistence and delivery

- Nothing persisted, nothing audited (read-only).
- The search runs inside the request. Workers are owned by the request's errgroup and stop when the request ends.
- **Shell invariants:**
  - S1: no git process outlives the request;
  - S2: never more than 8 concurrent `git grep` processes per request;
  - S3: only active tracked repos are searched (pinned by a test with an untracked repo that contains the pattern).

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| HTTP `POST /api/search` | `search.ParseQuery` | `Query` | as in Failures |
| `git grep -n --column --null` output | `search.ParseGrep` (streaming) | `Match` values | lines without the NUL-separated path, non-numeric line or column (skipped) |
| editor setting | closed set `vscode`, `cursor`, `idea` | `Editor` | anything else |

Each parser gets round-trip, accepted ⇒ valid, one test per rejection rule, and fuzz: `FuzzParseQuery`, `FuzzParseGrep`. `EditorURL` gets a property test that the output always parses as a URL with the right scheme and contains the path percent-encoded.

## Files

- **Core, `internal/core/search`:** `search.go` (Query, Search, Match, `Begin`, `Merge`, `Expire`, `EditorURL`) and `search_prop_test.go` (C1–C5).
- **Shell:**
  - `internal/search/service.go`;
  - `internal/search/parse.go` and its fuzz tests;
  - `internal/git/grep.go` (read-only `Grep`);
  - `internal/search/searchtest/` (fake + `GrepperContract`).
- **`internal/api`:** `search.go` (handler) and the DTOs in a new `types_search.go`.

## Open questions

1. **Streaming results to the browser:** not in Phase 2. One response is returned within 15 s. *Recommend*: add a WS stream later only if real projects need it.
2. **Default scope:** every active repo of the current project. On the home page, all active repos. *Recommend as stated.*
