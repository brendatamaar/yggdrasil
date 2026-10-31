/** Full-page message when the server refuses the browser for lack of a session. No retry loop. */
export function SessionRequiredPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-3 p-6" role="alert">
      <h1 className="text-xl font-semibold">Open Yggdrasil from the link in the terminal</h1>
      <p>
        This browser tab has no session. Use the link <code>ygg</code> printed when it started, which looks like{" "}
        <code>http://127.0.0.1:&lt;port&gt;/?k=...</code>.
      </p>
      <p className="text-muted">Each time Yggdrasil restarts it prints a new link, so an old tab needs the new one.</p>
    </main>
  );
}
