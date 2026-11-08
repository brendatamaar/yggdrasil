import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import {
  fetchDevServers,
  fetchServerLog,
  parseServerChanged,
  parseServerLog,
  runServerAction,
  type ServerAction,
} from "../../api/devservers";
import { TopicDevServerPrefix, TopicDevServers, type DevServerDTO, type DevServerLogResponse } from "../../api/gen/api.gen";
import { useWS } from "../../api/wsContext";
import { LOG_TAIL, mergeLog, upsertServer } from "./core";

export const devServerKeys = {
  all: ["devservers"] as const,
  list: ["devservers", "list"] as const,
  log: (id: string) => ["devservers", "log", id] as const,
};

/** Every dev server, kept fresh by the "devservers" topic. */
export function useDevServers() {
  const ws = useWS();
  const qc = useQueryClient();
  useEffect(
    () =>
      ws.subscribe(TopicDevServers, {
        onEvent: (msg) => {
          const server = parseServerChanged(msg.data);
          if (server) qc.setQueryData<DevServerDTO[]>(devServerKeys.list, (old) => upsertServer(old, server));
        },
        onGap: () => {
          void qc.invalidateQueries({ queryKey: devServerKeys.list });
        },
      }),
    [ws, qc],
  );
  return useQuery({ queryKey: devServerKeys.list, queryFn: fetchDevServers });
}

/** Start, stop or restart one server. The new state arrives on the topic; the list is also refetched. */
export function useServerAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; action: ServerAction }) => runServerAction(v.id, v.action),
    onSuccess: () => qc.invalidateQueries({ queryKey: devServerKeys.list }),
  });
}

/** The log tail plus lines streamed on "devserver:<id>", merged by sequence number. */
export function useServerLog(id: string) {
  const ws = useWS();
  const qc = useQueryClient();
  useEffect(
    () =>
      ws.subscribe(`${TopicDevServerPrefix}${id}`, {
        onEvent: (msg) => {
          const line = parseServerLog(msg.data);
          if (line) {
            qc.setQueryData<DevServerLogResponse>(devServerKeys.log(id), (old) =>
              old ? { ...old, lines: mergeLog(old.lines, [line]) } : old,
            );
            return;
          }
          const server = parseServerChanged(msg.data);
          if (server) qc.setQueryData<DevServerDTO[]>(devServerKeys.list, (old) => upsertServer(old, server));
        },
        // Lines were missed: load the tail again and merge it in.
        onGap: () => {
          void fetchServerLog(id, LOG_TAIL).then((fresh) => {
            qc.setQueryData<DevServerLogResponse>(devServerKeys.log(id), (old) => ({
              incomplete: fresh.incomplete || (old?.incomplete ?? false),
              lines: mergeLog(old?.lines ?? [], fresh.lines),
            }));
          }).catch(() => undefined);
        },
      }),
    [ws, qc, id],
  );
  return useQuery({ queryKey: devServerKeys.log(id), queryFn: () => fetchServerLog(id, LOG_TAIL), staleTime: Infinity });
}
