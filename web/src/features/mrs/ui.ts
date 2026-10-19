import type { InboxInstanceDTO } from "../../api/gen/api.gen";
import { relativeTime } from "../repos/ui";

export function phaseText(i: InboxInstanceDTO): string {
  switch (i.phase) {
    case "blocked":
      switch (i.blockReason) {
        case "unauthorized":
          return "Blocked: the token was refused. Check .env.";
        case "not_found":
          return "Blocked: the host wasn't found. Check its address in .env.";
        case "unsupported":
          return "Blocked: this host version isn't supported.";
        default:
          return "Blocked: Yggdrasil can't read this host. Check .env, then refresh.";
      }
    case "retrying":
      return i.retryAt
        ? `Problem reaching the host. Trying again at ${new Date(i.retryAt).toLocaleTimeString()}.`
        : "Problem reaching the host. Trying again soon.";
    case "fetching":
      return "Updating...";
    default:
      return i.fetchedAt ? `Up to date, ${relativeTime(i.fetchedAt)}` : "Up to date";
  }
}

