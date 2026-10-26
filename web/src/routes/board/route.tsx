import { createRoute } from "@tanstack/react-router";
import { BoardPage } from "../../features/board/BoardPage";
import { rootRoute } from "../root";

export const boardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/board",
  component: BoardPage,
});
