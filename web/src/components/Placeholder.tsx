export function Placeholder({ title, children }: { title: string; children?: string }) {
  return (
    <section aria-labelledby="page-title" className="space-y-2">
      <h1 id="page-title" className="text-lg font-semibold">
        {title}
      </h1>
      {children ? <p className="text-muted">{children}</p> : null}
    </section>
  );
}
