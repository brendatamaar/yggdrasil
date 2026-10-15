import { Link } from "@tanstack/react-router";
import { useCommandStore } from "../stores/commands";
import { NotificationsMenu } from "./NotificationsMenu";
import { StatusPill } from "./StatusPill";
import { ThemeToggle } from "./ThemeToggle";

const navClass = "rounded-sm px-2 py-1 text-sm text-muted hover:text-fg [&.active]:text-fg";

export function TopBar() {
  const openPalette = useCommandStore((s) => s.setOpen);

  return (
    <header className="flex h-12 items-center justify-between gap-4 border-b border-line px-4">
      <nav aria-label="Main" className="flex items-center gap-2">
        <Link to="/" className="rounded-sm pr-2 text-base font-semibold">
          Yggdrasil
        </Link>
        <Link to="/projects" className={navClass}>
          Projects
        </Link>
        <Link to="/settings" className={navClass}>
          Settings
        </Link>
      </nav>
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => {
            openPalette(true);
          }}
          aria-keyshortcuts="Control+K"
          className="rounded-md border border-line px-3 py-1 text-sm text-muted hover:bg-surface"
        >
          Search commands <kbd className="ml-1 text-xs">Ctrl+K</kbd>
        </button>
        <StatusPill />
        <NotificationsMenu />
        <ThemeToggle />
      </div>
    </header>
  );
}
