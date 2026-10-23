import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { describeError } from "../../api/client";
import { TopicHosts } from "../../api/gen/api.gen";
import type { InboxItemDTO } from "../../api/gen/api.gen";
import { fetchHostConfig, fetchInbox, refreshInbox } from "../../api/mrs";
import { useWS } from "../../api/wsContext";
import { SafeLink } from "../../components/SafeLink";
import { btn } from "../repos/ui";
import { phaseText } from "./ui";

const roles: { id: string; label: string }[] = [
  { id: "review_requested", label: "Review requested" },
  { id: "assigned", label: "Assigned to you" },
  { id: "authored", label: "Authored by you" },
];

const mrKeys = { inbox: ["mrs", "inbox"] as const, hosts: ["mrs", "hosts"] as const };

function Item({ it }: { it: InboxItemDTO }) {
  return (
    <li className="flex flex-wrap items-baseline gap-2 border-t border-line py-1 text-sm">
      <span className="text-muted">{it.repo}</span>
      <SafeLink href={it.mr.url} className="underline">
        {it.mr.title}
      </SafeLink>
      <span className="rounded-sm border border-line px-1 text-xs">{it.mr.state}</span>
      <span className="ml-auto text-xs text-muted">{it.instance}</span>
    </li>
  );
}

export function MrInboxPage() {
  const ws = useWS();
  const qc = useQueryClient();
  const inbox = useQuery({ queryKey: mrKeys.inbox, queryFn: fetchInbox });
  const hosts = useQuery({ queryKey: mrKeys.hosts, queryFn: fetchHostConfig });
  const refresh = useMutation({
    mutationFn: () => refreshInbox(),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["mrs"] }),
  });

  useEffect(() => {
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: ["mrs"] });
    };
    return ws.subscribe(TopicHosts, { onEvent: refetch, onGap: refetch });
  }, [ws, qc]);

  const items = inbox.data?.items ?? [];
  const instances = inbox.data?.instances ?? [];

  return (
    <div className="flex flex-col gap-4 p-4">
      <div className="flex items-center gap-3">
        <h1 className="text-lg font-semibold">Merge requests</h1>
        <button
          type="button"
          className={`${btn} ml-auto`}
          disabled={refresh.isPending}
          onClick={() => {
            refresh.mutate();
          }}
        >
          Refresh
        </button>
      </div>
      {(inbox.isError || refresh.isError) && (
        <p role="alert" className="text-sm text-red-500">
          {describeError(inbox.error ?? refresh.error)}
        </p>
      )}

      {instances.length > 0 && (
        <ul aria-label="Hosts" className="flex flex-col gap-1 text-sm">
          {instances.map((i) => (
            <li key={i.instance}>
              <span className="font-medium">{i.instance}</span>{" "}
              <span className={i.phase === "blocked" ? "text-red-500" : "text-muted"}>{phaseText(i)}</span>
            </li>
          ))}
        </ul>
      )}

      {items.length === 0 && inbox.data && (
        <div className="text-sm text-muted">
          <p>No merge requests to show.</p>
          <p>
            Tokens are optional: without one, Yggdrasil still gives you links to each repo&apos;s merge requests.
            To see your inbox here, add host settings and tokens to{" "}
            <code>{hosts.data?.envPath ?? ".env"}</code>.
          </p>
          {hosts.data && hosts.data.instances.length === 0 && <p>No git hosts are configured yet.</p>}
          {hosts.data?.configProblems.map((p) => <p key={p}>Skipped: {p}</p>)}
        </div>
      )}

      {roles.map((r) => {
        const group = items.filter((it) => it.role === r.id);
        if (group.length === 0) return null;
        return (
          <section key={r.id} aria-label={r.label}>
            <h2 className="font-semibold">
              {r.label} <span className="text-muted">({String(group.length)})</span>
            </h2>
            <ul>
              {group.map((it) => (
                <Item key={it.mr.url} it={it} />
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}
