import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { isTheme, THEME_LABELS, THEMES, useThemeStore } from "../stores/theme";

/** Theme picker (UI-05): a Radix radio menu, operable with the keyboard. */
export function ThemeToggle() {
  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={`Theme: ${THEME_LABELS[theme]}. Change theme`}
          className="rounded-md border border-line px-3 py-1 text-sm hover:bg-surface"
        >
          {THEME_LABELS[theme]}
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          className="z-50 min-w-40 rounded-md border border-line bg-canvas p-1 text-sm shadow-lg"
        >
          <DropdownMenu.RadioGroup
            value={theme}
            onValueChange={(v) => {
              if (isTheme(v)) setTheme(v);
            }}
          >
            {THEMES.map((t) => (
              <DropdownMenu.RadioItem
                key={t}
                value={t}
                className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 outline-none data-[highlighted]:bg-surface"
              >
                <span className="w-3 text-accent">
                  <DropdownMenu.ItemIndicator>✓</DropdownMenu.ItemIndicator>
                </span>
                {THEME_LABELS[t]}
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
