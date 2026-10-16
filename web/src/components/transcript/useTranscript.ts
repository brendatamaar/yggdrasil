import { useEffect, useRef, useState } from "react";
import { describeError } from "../../api/client";
import { TopicTaskPrefix } from "../../api/gen/api.gen";
import type { Event as AgentEvent } from "../../api/gen/agents.gen";
import { fetchTaskEvents } from "../../api/queries";
import { useWS } from "../../api/wsContext";
import { asEvent, contiguousSeq, hasGap, mergeEvents } from "./merge";

export interface TranscriptState {
  events: AgentEvent[];
  /** The first backfill has finished. */
  loaded: boolean;
  /** Plain-language message when events could not be loaded. */
  error: string | null;
}

/**
 * The events of one task, live. It subscribes first, then backfills over REST,
 * so nothing falls in between: the server stores an event before it sends it.
 * Live events and backfilled ones merge by `seq`. After a gap (a missed
 * message, a reconnect) it fetches again from the last event it has in an
 * unbroken run.
 */
export function useTranscript(taskId: string): TranscriptState {
  const ws = useWS();
  const [state, setState] = useState<TranscriptState>({ events: [], loaded: false, error: null });
  const eventsRef = useRef<AgentEvent[]>([]);

  useEffect(() => {
    let cancelled = false;
    let inflight = false;
    let again = false;
    // Another sync() call sets `again` while this one awaits; reading it through
    // a function keeps TypeScript from assuming it is still false.
    const askedAgain = () => again;
    eventsRef.current = [];
    setState({ events: [], loaded: false, error: null });

    const merge = (incoming: AgentEvent[]) => {
      eventsRef.current = mergeEvents(eventsRef.current, incoming);
      setState((s) => ({ ...s, events: eventsRef.current }));
    };

    const sync = async () => {
      if (inflight) {
        again = true;
        return;
      }
      inflight = true;
      try {
        do {
          again = false;
          const batch = await fetchTaskEvents(taskId, contiguousSeq(eventsRef.current));
          if (cancelled) return;
          merge(batch);
        } while (askedAgain());
        setState((s) => ({ ...s, loaded: true, error: null }));
      } catch (err) {
        if (!cancelled) setState((s) => ({ ...s, loaded: true, error: describeError(err) }));
      } finally {
        inflight = false;
      }
    };

    const unsubscribe = ws.subscribe(`${TopicTaskPrefix}${taskId}`, {
      onEvent: (msg) => {
        const ev = asEvent(msg.data);
        if (!ev || cancelled) return;
        merge([ev]);
        // The first live event can be ahead of what we hold: fetch the rest.
        if (hasGap(eventsRef.current)) void sync();
      },
      onGap: () => {
        void sync();
      },
    });
    void sync();

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [ws, taskId]);

  return state;
}
