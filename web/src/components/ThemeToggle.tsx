import * as Tooltip from "@radix-ui/react-tooltip";
import { useThemeStore, type Theme } from "../stores/theme";

const themeLabels: Record<Theme, string> = {
  system: "System",
  harbour: "Harbour",
};

export function ThemeToggle() {
  const theme = useThemeStore((state) => state.theme);
  const cycleTheme = useThemeStore((state) => state.cycleTheme);

  return (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>
        <button
          type="button"
          onClick={cycleTheme}
          aria-label={`Theme: ${themeLabels[theme]}. Change theme`}
          className="rounded-md border border-line px-3 py-1 text-sm hover:bg-surface"
        >
          {themeLabels[theme]}
        </button>
      </Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content sideOffset={6} className="rounded-md bg-fg px-2 py-1 text-xs text-canvas shadow">
          Change theme (system, Harbour)
          <Tooltip.Arrow className="fill-fg" />
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}
