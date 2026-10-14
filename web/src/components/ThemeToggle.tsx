import * as Tooltip from "@radix-ui/react-tooltip";
import { useThemeStore, type Theme } from "../stores/theme";

const labels: Record<Theme, string> = {
  system: "System",
  dark: "Dark",
  light: "Light",
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
          aria-label={`Theme: ${labels[theme]}. Change theme`}
          className="rounded-md border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100 dark:border-zinc-700 dark:hover:bg-zinc-800"
        >
          {labels[theme]}
        </button>
      </Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content
          sideOffset={6}
          className="rounded-md bg-zinc-900 px-2 py-1 text-xs text-zinc-50 shadow dark:bg-zinc-100 dark:text-zinc-900"
        >
          Change theme (system, dark, light)
          <Tooltip.Arrow className="fill-zinc-900 dark:fill-zinc-100" />
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}
