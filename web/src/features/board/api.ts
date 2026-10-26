import { apiFetch } from "../../api/client";
import type { BoardResponse } from "../../api/gen/api.gen";
import { cancelTask } from "../../api/queries";

export { cancelTask };

export const fetchBoard = () => apiFetch<BoardResponse>("/api/board");
