import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <section aria-labelledby="not-found-title" className="space-y-2">
      <h1 id="not-found-title" className="text-lg font-semibold">
        Page not found
      </h1>
      <p>
        <Link to="/" className="rounded-sm underline">
          Go to the start page
        </Link>
      </p>
    </section>
  );
}
