import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, describeError } from "../../api/client";
import { registerCommands, type Command } from "../../stores/commands";
import { pickAgent, useAgents } from "../composer/agents";
import * as api from "./api";
import { kindTitle } from "./attention";
import { stepAction, type ActionCommand, type ActionInput, type ActionState, type ActionTarget } from "./core";
import type { ActionOfferingDTO } from "../../api/gen/api.gen";

export const actionKeys = {
  all: ["actions"] as const,
  repo: (repoId: string) => ["actions", "repo", repoId] as const,
};

/** The server's action catalogue for one tracked repo. */
export function useRepoActions(repoId: string, enabled = true) {
  return useQuery({
    queryKey: actionKeys.repo(repoId),
    queryFn: ({ signal }) => api.fetchRepoActions(repoId, signal),
    enabled,
  });
}

/** A key for one submission, generated once; the server dedups on it. */
export function newRequestId(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  return `req-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/** A refusal the server gave (4xx): the request did not run. Anything else (network, 5xx) is an unknown outcome. */
export const isDefiniteRefusal = (err: unknown): boolean => err instanceof ApiError && err.status >= 400 && err.status < 500;

const RECONCILE_TRIES = 5;
const RECONCILE_DELAY_MS = 1000;

export interface ActionMachine {
  state: ActionState;
  /** Opens the dialog for an offering; the default agent is filled in when the action runs one. */
  open: (target: ActionTarget) => void;
  submit: () => void;
  retry: () => void;
  close: () => void;
}

// The server asks for an agent whenever a task will exist (Ask, Code, draft); a pull proposal runs none.
const NO_AGENT = new Set(["pull_and_check"]);

/**
 * Runs the action-dialog reducer. The component and the palette only dispatch inputs; the
 * commands (preview query, the one explicit submission, reconcile, invalidate) run here.
 */
export function useActionMachine(): ActionMachine {
  const qc = useQueryClient();
  const agents = useAgents();
  const [state, setState] = useState<ActionState>({ tag: "closed" });
  const stateRef = useRef<ActionState>(state);
  const aborts = useRef(new Map<string, AbortController>());
  const alive = useRef(true);
  const agentRef = useRef("");
  const defaultAgent = pickAgent(agents.data ?? [], "");
  useEffect(() => {
    agentRef.current = defaultAgent;
  }, [defaultAgent]);

  useEffect(() => {
    alive.current = true;
    const map = aborts.current;
    return () => {
      alive.current = false;
      map.forEach((c) => {
        c.abort();
      });
      map.clear();
    };
  }, []);

  // `dispatch` and `run` call each other: functions, not hooks, held in refs so both are stable.
  const dispatchRef = useRef<(i: ActionInput) => void>(() => undefined);

  const run = useCallback(
    (c: ActionCommand) => {
      const dispatch = (i: ActionInput) => {
        dispatchRef.current(i);
      };
      switch (c.tag) {
        case "queryPreview": {
          const ctl = new AbortController();
          aborts.current.set(c.request.requestId, ctl);
          api
            .previewAction(c.request, ctl.signal)
            .then((preview) => {
              dispatch({ tag: "loaded", requestId: c.request.requestId, preview });
            })
            .catch((err: unknown) => {
              if (ctl.signal.aborted) return;
              dispatch({ tag: "loadFailed", requestId: c.request.requestId, message: describeActionError(err) });
            })
            .finally(() => {
              aborts.current.delete(c.request.requestId);
            });
          return;
        }
        case "cancelQuery":
          aborts.current.get(c.requestId)?.abort();
          aborts.current.delete(c.requestId);
          return;
        case "submitAction":
          // The digest and head come from the preview; the type needs both fields to be present.
          api
            .executeAction({ ...c.request, digest: c.digest, ...(c.expectedHead ? { expectedHead: c.expectedHead } : {}) })
            .then((result) => {
              dispatch({ tag: "submitted", requestId: c.request.requestId, result });
            })
            .catch((err: unknown) => {
              dispatch({
                tag: "submitFailed",
                requestId: c.request.requestId,
                message: describeActionError(err),
                definite: isDefiniteRefusal(err),
              });
            });
          return;
        case "reconcile": {
          void (async () => {
            for (let i = 0; i < RECONCILE_TRIES; i++) {
              await new Promise((r) => setTimeout(r, RECONCILE_DELAY_MS));
              if (!alive.current) return;
              try {
                const result = await api.getAction(c.requestId);
                if (result.state !== "submitting") {
                  dispatch({ tag: "submitted", requestId: c.requestId, result });
                  return;
                }
              } catch (err) {
                if (isDefiniteRefusal(err)) {
                  dispatch({ tag: "submitFailed", requestId: c.requestId, message: describeActionError(err), definite: true });
                  return;
                }
              }
            }
            if (alive.current) {
              dispatch({
                tag: "submitFailed",
                requestId: c.requestId,
                message: "Still waiting for an answer. Retrying checks the same request; it never starts a second one.",
                definite: false,
              });
            }
          })();
          return;
        }
        case "invalidate":
          for (const key of [["repos"], ["actions"], ["proposals"], ["tasks"], ["board"], ["builds"]]) {
            void qc.invalidateQueries({ queryKey: key });
          }
          return;
      }
    },
    [qc],
  );

  const dispatch = useCallback(
    (input: ActionInput) => {
      const [next, cmds] = stepAction(stateRef.current, input);
      stateRef.current = next;
      setState(next);
      cmds.forEach(run);
    },
    [run],
  );
  useEffect(() => {
    dispatchRef.current = dispatch;
  }, [dispatch]);

  const open = useCallback(
    (target: ActionTarget) => {
      const agent = NO_AGENT.has(target.kind) ? undefined : (target.agent ?? (agentRef.current || undefined));
      dispatch({ tag: "open", target: { ...target, ...(agent ? { agent } : {}) }, requestId: newRequestId() });
    },
    [dispatch],
  );
  const submit = useCallback(() => {
    dispatch({ tag: "submit" });
  }, [dispatch]);
  const retry = useCallback(() => {
    dispatch({ tag: "retry", requestId: newRequestId() });
  }, [dispatch]);
  const close = useCallback(() => {
    dispatch({ tag: "close" });
  }, [dispatch]);

  return useMemo(() => ({ state, open, submit, retry, close }), [state, open, submit, retry, close]);
}

export function describeActionError(err: unknown): string {
  if (err instanceof api.ActionDecodeError) return err.message;
  return describeError(err);
}

/** The offering as a dialog target. */
export function targetOf(repoId: string, o: ActionOfferingDTO, repoName?: string): ActionTarget {
  return {
    repoId,
    kind: o.kind,
    title: o.title || kindTitle(o.kind),
    ...(o.sourceId ? { sourceId: o.sourceId } : {}),
    ...(repoName ? { repoName } : {}),
  };
}

/**
 * Registers one palette command per offering while the caller is mounted (UI-04). A command
 * only opens the preview: confirmation is never bypassed. Ids are stable per repo and action.
 */
export function useActionPaletteCommands(
  repoId: string,
  repoName: string,
  offerings: readonly ActionOfferingDTO[],
  open: (t: ActionTarget) => void,
): void {
  const commands = useMemo<Command[]>(
    () =>
      offerings.map((o) => ({
        id: `action:${repoId}:${o.kind}:${o.sourceId ?? ""}`,
        title: `${o.title || kindTitle(o.kind)}: ${repoName}`,
        group: "Actions",
        keywords: [repoName, o.kind, o.reason],
        run: () => {
          open(targetOf(repoId, o, repoName));
        },
      })),
    [repoId, repoName, offerings, open],
  );
  useEffect(() => registerCommands(commands), [commands]);
}
