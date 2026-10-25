import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { fetchInbox } from "../../api/mrs";
import { SafeLink } from "../../components/SafeLink";

const roleLabel: Record<string, string> = {
  review_requested: "Review requested",
  assigned: "Assigned",
  authored: "Yours",
};
const rank = (role: string) => (role === "review_requested" ? 0 : role === "assigned" ? 1 : 2);
const LIMIT = 8;

/** Open merge requests that need you: review requests first. */
export function MergeRequests() {
  // Same key as the MR inbox page, so the two share one cache entry.
  const { data, error, isPending } = useQuery({ queryKey: ["mrs", "inbox"], queryFn: fetchInbox });

  if (isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(error)}
      </p>
    );
  }
  const items = data.items
    .filter((it) => it.mr.state === "open" || it.mr.state === "draft")
    .sort((a, b) => rank(a.role) - rank(b.role));
  if (items.length === 0) {
    return (
      <p className="text-sm text-muted">
        No open merge requests.{" "}
        <Link to="/mrs" className="text-accent hover:underline">
          Open the inbox
        </Link>{" "}
        to set up git hosts.
      </p>
    );
  }
  return (
    <ul className="space-y-1 text-sm">
      {items.slice(0, LIMIT).map((it) => (
        <li key={it.mr.url} className="flex flex-wrap items-baseline gap-2">
          <span className="text-muted">{it.repo}</span>
          <SafeLink href={it.mr.url} className="text-accent hover:underline">
            {it.mr.title}
          </SafeLink>
          <span className="ml-auto rounded-sm border border-line px-1 text-xs">
            {roleLabel[it.role] ?? it.role}
            {it.mr.state === "draft" ? ", draft" : ""}
          </span>
        </li>
      ))}
    </ul>
  );
}
