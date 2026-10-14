import { Link } from "@tanstack/react-router";
import { ThemeToggle } from "./ThemeToggle";

export function TopBar() {
  return (
    <header className="flex h-12 items-center justify-between border-b border-zinc-200 px-4 dark:border-zinc-800">
      <Link to="/" className="rounded-sm text-base font-semibold">
        Yggdrasil
      </Link>
      <ThemeToggle />
    </header>
  );
}
