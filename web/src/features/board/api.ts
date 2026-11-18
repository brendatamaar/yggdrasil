import { apiFetch } from "../../api/client";
import type { BoardCardDTO, BoardResponse } from "../../api/gen/api.gen";
import { cancelTask, isAnchorAgent } from "../../api/queries";

export { cancelTask };

/** The cards a person can see: the internal anchor drafts that only hold proposals are left out. */
export const withoutAnchors = (cards: readonly BoardCardDTO[]): BoardCardDTO[] => cards.filter((c) => !isAnchorAgent(c.agent));

export const fetchBoard = async (): Promise<BoardResponse> => {
  const board = await apiFetch<BoardResponse>("/api/board");
  return { ...board, cards: withoutAnchors(board.cards) };
};
