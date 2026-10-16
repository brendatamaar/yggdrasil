import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import GridLayout, { WidthProvider, type Layout } from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import { useLayoutStore } from "../../stores/layout";
import { getWidget, widgetRegistry } from "../../widgets/registry";
import { GRID_COLUMNS, sameLayout, type LayoutItem } from "./layout";

const Grid = WidthProvider(GridLayout);

const buttonClass = "rounded border border-line px-2 py-1 text-sm hover:bg-surface";

/** The home page's widget grid (UI-02), with a Customize mode to arrange it. */
export function WidgetGrid() {
  const items = useLayoutStore((s) => s.items);
  const customizing = useLayoutStore((s) => s.customizing);
  const { setItems, add, remove, reset, setCustomizing } = useLayoutStore.getState();

  const placed = new Set(items.map((i) => i.id));
  const available = widgetRegistry.filter((w) => !placed.has(w.id));

  const layout: Layout[] = items.flatMap((i) => {
    const mod = getWidget(i.id);
    return mod
      ? [{ i: i.id, x: i.x, y: i.y, w: i.w, h: i.h, minW: mod.defaultSize.minW, minH: mod.defaultSize.minH }]
      : [];
  });

  const onLayoutChange = (next: Layout[]) => {
    const updated: LayoutItem[] = next.map(({ i, x, y, w, h }) => ({ id: i, x, y, w, h }));
    // React-grid-layout reports the layout on mount too; only save a real change.
    if (!sameLayout(updated, items)) setItems(updated);
  };

  return (
    <section aria-label="Widgets" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className={buttonClass}
          aria-pressed={customizing}
          onClick={() => {
            setCustomizing(!customizing);
          }}
        >
          {customizing ? "Done" : "Customize"}
        </button>
        {customizing ? (
          <>
            <DropdownMenu.Root>
              <DropdownMenu.Trigger className={buttonClass} disabled={available.length === 0}>
                Add widget
              </DropdownMenu.Trigger>
              <DropdownMenu.Portal>
                <DropdownMenu.Content
                  align="start"
                  className="min-w-40 rounded border border-line bg-canvas p-1 shadow-md"
                >
                  {available.map((w) => (
                    <DropdownMenu.Item
                      key={w.id}
                      className="cursor-pointer rounded px-2 py-1 text-sm outline-none data-[highlighted]:bg-surface"
                      onSelect={() => {
                        add(w);
                      }}
                    >
                      {w.title}
                    </DropdownMenu.Item>
                  ))}
                </DropdownMenu.Content>
              </DropdownMenu.Portal>
            </DropdownMenu.Root>
            <button type="button" className={buttonClass} onClick={reset}>
              Reset layout
            </button>
          </>
        ) : null}
      </div>

      {items.length === 0 ? (
        <p className="text-sm text-muted">No widgets here. Choose Customize, then Add widget.</p>
      ) : (
        <Grid
          layout={layout}
          cols={GRID_COLUMNS}
          rowHeight={64}
          margin={[12, 12]}
          isDraggable={customizing}
          isResizable={customizing}
          draggableHandle=".widget-handle"
          draggableCancel=".widget-no-drag"
          onLayoutChange={onLayoutChange}
        >
          {layout.map(({ i }) => {
            const mod = getWidget(i);
            if (!mod) return null;
            return (
              <div key={i} className="flex flex-col overflow-hidden rounded border border-line bg-surface">
                <div
                  className={`widget-handle flex items-center justify-between border-b border-line px-3 py-1 text-sm font-medium ${customizing ? "cursor-move" : ""}`}
                >
                  <h2>{mod.title}</h2>
                  {customizing ? (
                    <button
                      type="button"
                      className="widget-no-drag text-xs text-muted hover:text-fg"
                      aria-label={`Remove ${mod.title}`}
                      onClick={() => {
                        remove(i);
                      }}
                    >
                      Remove
                    </button>
                  ) : null}
                </div>
                <div className="flex-1 overflow-auto p-3">
                  <mod.Component />
                </div>
              </div>
            );
          })}
        </Grid>
      )}
    </section>
  );
}
