import { createRoute } from "@tanstack/react-router";
import { Composer } from "../components/Composer";
import { WidgetGrid } from "../components/widgetgrid/WidgetGrid";
import { rootRoute } from "./root";

export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: function Start() {
    return (
      <div className="space-y-6">
        <h1 className="sr-only">Start</h1>
        <Composer />
        <WidgetGrid />
      </div>
    );
  },
});
