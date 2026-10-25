import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { describeError } from "../../api/client";
import type { SearchMatchDTO, SearchProblemDTO, SearchRequest } from "../../api/gen/api.gen";
import { fetchRepos } from "../../api/repos";
import { searchCode } from "../../api/search";
import { btn, btnPrimary } from "../repos/ui";
import { safeEditorHref } from "./links";

const input = "rounded-md border border-line bg-canvas px-2 py-1 text-sm";

function problemText(p: SearchProblemDTO): string {
  switch (p.kind) {
    case "timed_out":
      return "The search took too long here, so results may be missing.";
    case "git_failed":
      return "Git could not search this repo.";
    case "missing":
      return "This repo's folder is missing.";
    default:
      return "This repo could not be searched.";
  }
}

function groupMatches(matches: SearchMatchDTO[]) {
  const repos = new Map<string, { name: string; files: Map<string, SearchMatchDTO[]> }>();
  for (const m of matches) {
    let r = repos.get(m.repo);
    if (!r) {
      r = { name: m.repoName, files: new Map() };
      repos.set(m.repo, r);
    }
    const list = r.files.get(m.path) ?? [];
    list.push(m);
    r.files.set(m.path, list);
  }
  return [...repos.entries()];
}

function MatchRow({ m }: { m: SearchMatchDTO }) {
  const href = safeEditorHref(m.editorUrl);
  return (
    <li
      tabIndex={0}
      data-result
      className="flex gap-3 rounded-sm px-1 py-0.5 font-mono text-xs focus:bg-surface focus:outline focus:outline-1 focus:outline-accent"
    >
      {href ? (
        <a href={href} className="w-12 shrink-0 text-right underline" aria-label={`Open line ${String(m.line)} in editor`}>
          {m.line}
        </a>
      ) : (
        <span className="w-12 shrink-0 text-right text-muted">{m.line}</span>
      )}
      <span className="whitespace-pre-wrap break-all">{m.text.trim()}</span>
    </li>
  );
}

export function SearchPage() {
  const boxRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const [pattern, setPattern] = useState("");
  const [regex, setRegex] = useState(false);
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [paths, setPaths] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());

  useEffect(() => {
    boxRef.current?.focus();
  }, []);

  const repos = useQuery({ queryKey: ["repos", "all"], queryFn: () => fetchRepos("all") });
  const repoList = useMemo(() => (repos.data?.projects ?? []).flatMap((p) => p.repos), [repos.data]);

  const search = useMutation({ mutationFn: (req: SearchRequest) => searchCode(req) });

  const run = () => {
    if (pattern.trim() === "") return;
    const globs = paths
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter((s) => s !== "");
    search.mutate({
      pattern,
      mode: regex ? "regex" : "literal",
      case: caseSensitive,
      repos: [...picked],
      paths: globs,
    });
  };

  const toggleRepo = (id: string) => {
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const focusResult = (delta: 1 | -1) => {
    const items = [...(resultsRef.current?.querySelectorAll<HTMLElement>("[data-result]") ?? [])];
    const cur = items.findIndex((el) => el === document.activeElement || el.contains(document.activeElement));
    const target = items[cur + delta];
    if (target) {
      target.focus();
      return true;
    }
    return false;
  };

  const onResultsKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    if (!focusResult(e.key === "ArrowDown" ? 1 : -1) && e.key === "ArrowUp") boxRef.current?.focus();
  };

  const data = search.data;
  const groups = data ? groupMatches(data.matches) : [];

  return (
    <section aria-label="Code search" className="flex flex-col gap-3 p-4">
      <h1 className="text-lg font-semibold">Search code</h1>
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          run();
        }}
      >
        <div className="flex flex-wrap items-center gap-2">
          <input
            ref={boxRef}
            type="text"
            aria-label="Search pattern"
            placeholder="Search tracked repos"
            value={pattern}
            onChange={(e) => {
              setPattern(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                const first = resultsRef.current?.querySelector<HTMLElement>("[data-result]");
                if (first) {
                  e.preventDefault();
                  first.focus();
                }
              }
            }}
            className={`${input} min-w-64 flex-1`}
          />
          <label className="flex items-center gap-1 text-sm">
            <input
              type="checkbox"
              checked={regex}
              onChange={(e) => {
                setRegex(e.target.checked);
              }}
            />
            Regex
          </label>
          <label className="flex items-center gap-1 text-sm">
            <input
              type="checkbox"
              checked={caseSensitive}
              onChange={(e) => {
                setCaseSensitive(e.target.checked);
              }}
            />
            Match case
          </label>
          <button type="submit" className={btnPrimary} disabled={search.isPending || pattern.trim() === ""}>
            Search
          </button>
        </div>
        <input
          type="text"
          aria-label="File patterns"
          placeholder="Only these files, e.g. *.java, src/**/*.ts"
          value={paths}
          onChange={(e) => {
            setPaths(e.target.value);
          }}
          className={input}
        />
        <fieldset className="flex flex-wrap items-center gap-3 text-sm">
          <legend className="sr-only">Repos</legend>
          <span className="text-muted">{picked.size === 0 ? "All active repos" : `${String(picked.size)} selected`}</span>
          {repoList.map((r) => (
            <label key={r.id} className="flex items-center gap-1">
              <input
                type="checkbox"
                checked={picked.has(r.id)}
                onChange={() => {
                  toggleRepo(r.id);
                }}
              />
              {r.name}
            </label>
          ))}
          {picked.size > 0 && (
            <button
              type="button"
              className={btn}
              onClick={() => {
                setPicked(new Set());
              }}
            >
              Clear
            </button>
          )}
        </fieldset>
      </form>

      <div ref={resultsRef} onKeyDown={onResultsKey} aria-live="polite" className="flex flex-col gap-3">
        {search.isPending && <p className="text-sm text-muted">Searching...</p>}
        {search.isError && (
          <p role="alert" className="text-sm text-danger">
            {describeError(search.error)}
          </p>
        )}
        {data && (
          <>
            {data.truncated && (
              <p role="status" className="rounded-md border border-line px-2 py-1 text-sm">
                Too many matches. Showing the first ones only; narrow the search to see the rest.
              </p>
            )}
            {data.problems.length > 0 && (
              <ul aria-label="Problems" className="flex flex-col gap-1 text-sm">
                {data.problems.map((p) => (
                  <li key={`${p.repo}-${p.kind}`}>
                    <strong>{p.repoName}</strong>: {problemText(p)} {p.message}
                  </li>
                ))}
              </ul>
            )}
            {groups.length === 0 && <p className="text-sm text-muted">No matches.</p>}
            {groups.map(([id, r]) => (
              <section key={id} aria-label={r.name} className="flex flex-col gap-1">
                <h2 className="text-sm font-semibold">{r.name}</h2>
                {[...r.files.entries()].map(([path, ms]) => (
                  <div key={path}>
                    <h3 className="font-mono text-xs text-muted">{path}</h3>
                    <ul>
                      {ms.map((m) => (
                        <MatchRow key={`${String(m.line)}:${String(m.col)}`} m={m} />
                      ))}
                    </ul>
                  </div>
                ))}
              </section>
            ))}
          </>
        )}
      </div>
    </section>
  );
}
