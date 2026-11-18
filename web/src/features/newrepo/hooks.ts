import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, describeError } from "../../api/client";
import { TopicNewRepos, type NewRepoOperationDTO } from "../../api/gen/api.gen";
import { fetchHostConfig } from "../../api/mrs";
import { WSContext } from "../../api/wsContext";
import { registerCommands } from "../../stores/commands";
import { proposalKeys } from "../proposals";
import * as api from "./api";
import { findPublishProposal, isActive, stepPrepare, stepRetry, type PrepareCommand, type PrepareInput, type PrepareState, type RetryCommand, type RetryInput, type RetryState } from "./core";

export const newRepoKeys = {
  all: ["newrepos"] as const,
  one: (id: string) => ["newrepos", "one", id] as const,
  project: (projectId: string) => ["newrepos", "project", projectId] as const,
  hosts: ["newrepos", "hosts"] as const,
};

/** One request key per submission; the server dedups on it. */
export function newRequestId(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  return `req-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/** A refusal the server gave (4xx): the request did not run. Network errors and 5xx are unknown outcomes. */
export const isDefiniteRefusal = (err: unknown): boolean => err instanceof ApiError && err.status >= 400 && err.status < 500;

export function describeNewRepoError(err: unknown): string {
  if (err instanceof api.OperationDecodeError) return err.message;
  return describeError(err);
}

/** Refreshes new-repo queries when the server reports a change on the "newrepos" topic. */
function useNewReposLive(): void {
  const ws = useContext(WSContext);
  const qc = useQueryClient();
  useEffect(() => {
    if (!ws) return;
    const refresh = () => {
      void qc.invalidateQueries({ queryKey: newRepoKeys.all });
    };
    return ws.subscribe(TopicNewRepos, { onEvent: refresh, onGap: refresh });
  }, [ws, qc]);
}

const ACTIVE_POLL_MS = 3000;

/** One operation, live: the topic invalidates it, and it is re-read while it is still moving. */
export function useOperation(id: string) {
  useNewReposLive();
  return useQuery({
    queryKey: newRepoKeys.one(id),
    queryFn: ({ signal }) => api.getNewRepo(id, signal),
    refetchInterval: (q) => (q.state.data && isActive(q.state.data) ? ACTIVE_POLL_MS : false),
  });
}

/** The project's new-repo operations (so one in progress can be reopened). */
export function useProjectOperations(projectId: string, enabled: boolean) {
  useNewReposLive();
  return useQuery({ queryKey: newRepoKeys.project(projectId), queryFn: ({ signal }) => api.listNewRepos(projectId, signal), enabled });
}

/**
 * The publish proposal this operation is waiting on, read from the server again (it is never held
 * only in the page), so a reload does not lose it. Undefined until found, or when there is none.
 */
export function usePublishProposalId(op: NewRepoOperationDTO): string | undefined {
  const watching = op.state === "ready" || op.state === "publishing";
  const q = useQuery({
    queryKey: proposalKeys.task(op.setupTaskId),
    queryFn: ({ signal }) => api.listSetupProposals(op.setupTaskId, signal),
    enabled: watching,
  });
  return watching && q.data ? findPublishProposal(op, q.data) : undefined;
}

/** Configured hosts, for choosing where to publish. */
export function useHostInstances(enabled: boolean) {
  return useQuery({ queryKey: newRepoKeys.hosts, queryFn: fetchHostConfig, enabled });
}

export interface PrepareMachine<F, P extends { hash: string }, R> {
  state: PrepareState<F, P, R>;
  preview: (form: F) => void;
  submit: () => void;
  edit: () => void;
  retry: () => void;
  reset: () => void;
}

interface PrepareDeps<F, P, R> {
  queryPreview: (form: F, signal: AbortSignal) => Promise<P>;
  /** Sends the one explicit submission with the hash from the preview that was shown. */
  send: (form: F, hash: string, requestId: string) => Promise<R>;
}

/**
 * Runs the prepare-flow reducer. Components only dispatch inputs; the commands (preview query,
 * the one explicit submission) run here.
 */
export function usePrepareMachine<F, P extends { hash: string }, R>(deps: PrepareDeps<F, P, R>): PrepareMachine<F, P, R> {
  const [state, setState] = useState<PrepareState<F, P, R>>({ tag: "editing" });
  const stateRef = useRef(state);
  const depsRef = useRef(deps);
  const aborts = useRef(new Map<string, AbortController>());
  const alive = useRef(true);
  const dispatchRef = useRef<(i: PrepareInput<F, P, R>) => void>(() => undefined);

  useEffect(() => {
    depsRef.current = deps;
  });
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

  const run = useCallback((c: PrepareCommand<F>) => {
    const dispatch = (i: PrepareInput<F, P, R>) => {
      if (alive.current) dispatchRef.current(i);
    };
    switch (c.tag) {
      case "queryPreview": {
        const ctl = new AbortController();
        aborts.current.set(c.requestId, ctl);
        depsRef.current
          .queryPreview(c.form, ctl.signal)
          .then((preview) => {
            dispatch({ tag: "previewLoaded", requestId: c.requestId, preview });
          })
          .catch((err: unknown) => {
            if (ctl.signal.aborted) return;
            dispatch({ tag: "previewFailed", requestId: c.requestId, message: describeNewRepoError(err) });
          })
          .finally(() => {
            aborts.current.delete(c.requestId);
          });
        return;
      }
      case "cancelPreview":
        aborts.current.get(c.requestId)?.abort();
        aborts.current.delete(c.requestId);
        return;
      case "submit":
        depsRef.current
          .send(c.form, c.hash, c.requestId)
          .then((result) => {
            dispatch({ tag: "submitted", requestId: c.requestId, result });
          })
          .catch((err: unknown) => {
            dispatch({ tag: "submitFailed", requestId: c.requestId, message: describeNewRepoError(err), definite: isDefiniteRefusal(err) });
          });
        return;
    }
  }, []);

  const dispatch = useCallback(
    (input: PrepareInput<F, P, R>) => {
      const [next, cmds] = stepPrepare(stateRef.current, input);
      stateRef.current = next;
      setState(next);
      cmds.forEach(run);
    },
    [run],
  );
  useEffect(() => {
    dispatchRef.current = dispatch;
  }, [dispatch]);

  return useMemo(
    () => ({
      state,
      preview: (form: F) => {
        dispatch({ tag: "preview", form, requestId: newRequestId() });
      },
      submit: () => {
        dispatch({ tag: "submit" });
      },
      edit: () => {
        dispatch({ tag: "edit" });
      },
      retry: () => {
        dispatch({ tag: "retry", requestId: newRequestId() });
      },
      reset: () => {
        dispatch({ tag: "reset" });
      },
    }),
    [state, dispatch],
  );
}

export interface RetryMachine {
  state: RetryState;
  open: (spec: { hash: string; lines: string[] } | undefined) => void;
  confirm: () => void;
  close: () => void;
}

/** Runs the retry-confirmation reducer: the one explicit POST carries the hash held in the state. */
export function useRetryMachine(operationId: string): RetryMachine {
  const qc = useQueryClient();
  const [state, setState] = useState<RetryState>({ tag: "closed" });
  const stateRef = useRef(state);
  const dispatchRef = useRef<(i: RetryInput) => void>(() => undefined);

  const run = useCallback(
    (c: RetryCommand) => {
      if (c.tag === "refresh") {
        void qc.invalidateQueries({ queryKey: newRepoKeys.all });
        return;
      }
      api
        .retryNewRepo(operationId, c.hash)
        .then(() => {
          dispatchRef.current({ tag: "succeeded" });
        })
        .catch((err: unknown) => {
          dispatchRef.current({ tag: "failed", message: describeNewRepoError(err) });
        });
    },
    [operationId, qc],
  );
  const dispatch = useCallback(
    (input: RetryInput) => {
      const [next, cmds] = stepRetry(stateRef.current, input);
      stateRef.current = next;
      setState(next);
      cmds.forEach(run);
    },
    [run],
  );
  useEffect(() => {
    dispatchRef.current = dispatch;
  }, [dispatch]);

  return useMemo(
    () => ({
      state,
      open: (spec) => {
        dispatch({ tag: "open", spec });
      },
      confirm: () => {
        dispatch({ tag: "confirm" });
      },
      close: () => {
        dispatch({ tag: "close" });
      },
    }),
    [state, dispatch],
  );
}

/** Registers the "New repo" palette command while the caller is mounted. It only opens the preview form. */
export function useNewRepoPaletteCommand(projectId: string, projectName: string, open: () => void): void {
  const commands = useMemo(
    () => [
      {
        id: `newrepo:${projectId}`,
        title: `New repo in ${projectName}`,
        group: "Actions",
        keywords: [projectName, "create", "init", "repository"],
        run: open,
      },
    ],
    [projectId, projectName, open],
  );
  useEffect(() => registerCommands(commands), [commands]);
}
