import { Outlet, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useRegisterCommands, type Command } from "../stores/commands";
import { useThemeStore } from "../stores/theme";
import { CommandPalette } from "./CommandPalette";
import { TopBar } from "./TopBar";

export function AppLayout() {
  const navigate = useNavigate();
  const cycleTheme = useThemeStore((s) => s.cycleTheme);

  const builtins = useMemo<Command[]>(
    () => [
      { id: "go.start", title: "Go to Start", group: "Navigate", run: () => void navigate({ to: "/" }) },
      { id: "go.projects", title: "Go to Projects", group: "Navigate", run: () => void navigate({ to: "/projects" }) },
      { id: "go.settings", title: "Go to Settings", group: "Navigate", run: () => void navigate({ to: "/settings" }) },
      { id: "theme.cycle", title: "Change theme", group: "Appearance", keywords: ["dark", "light"], run: cycleTheme },
    ],
    [navigate, cycleTheme],
  );
  useRegisterCommands(builtins);

  return (
    <div className="flex min-h-screen flex-col">
      <TopBar />
      <main className="flex-1 p-4">
        <Outlet />
      </main>
      <CommandPalette />
    </div>
  );
}
