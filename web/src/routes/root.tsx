import { createRootRoute } from "@tanstack/react-router";
import { AppLayout } from "../components/AppLayout";
import { NotFound } from "./NotFound";

export const rootRoute = createRootRoute({
  component: AppLayout,
  notFoundComponent: NotFound,
});
