import { Outlet } from "@tanstack/react-router";
import { TopBar } from "./TopBar";

export function AppLayout() {
  return (
    <div className="flex min-h-screen flex-col">
      <TopBar />
      <main className="flex-1 p-4">
        <Outlet />
      </main>
    </div>
  );
}
