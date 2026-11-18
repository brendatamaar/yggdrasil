import { describe, expect, it } from "vitest";
import type { NoticeDTO, NoticesResponse } from "../../api/gen/api.gen";
import {
  initialDelivery,
  mergeNotices,
  noticeText,
  stepDelivery,
  unreadIds,
  type DeliveryInput,
  type DeliveryState,
  type NoticeView,
} from "./core";

const granted = (over: Partial<Extract<DeliveryState, { tag: "granted" }>> = {}): DeliveryState => ({
  tag: "granted",
  visible: false,
  delivered: new Set(),
  ...over,
});
const withTag = (tag: DeliveryState["tag"], visible = false): DeliveryState => ({ tag, visible, delivered: new Set() });
const live = (id: string): DeliveryInput => ({ tag: "liveNotice", id, title: "Add cart page", status: "needs_you" });

describe("browser delivery reducer", () => {
  it("capabilityChecked sets the permission variant and keeps visibility and delivered IDs", () => {
    const s0 = { ...withTag("unsupported", false), delivered: new Set(["a"]) } as DeliveryState;
    const [s] = stepDelivery(s0, { tag: "capabilityChecked", permission: "default" });
    expect(s.tag).toBe("default");
    expect(s.visible).toBe(false);
    expect([...s.delivered]).toEqual(["a"]);
  });

  it("a permission gesture asks only while the answer is still open", () => {
    expect(stepDelivery(withTag("default"), { tag: "permissionGesture" })[1]).toEqual([{ tag: "requestPermission" }]);
    for (const tag of ["unsupported", "denied", "granted"] as const) {
      expect(stepDelivery(withTag(tag), { tag: "permissionGesture" })).toEqual([withTag(tag), []]);
    }
  });

  it("permissionResolved moves to the answer; unsupported ignores it", () => {
    expect(stepDelivery(withTag("default"), { tag: "permissionResolved", permission: "granted" })[0].tag).toBe("granted");
    expect(stepDelivery(withTag("default"), { tag: "permissionResolved", permission: "denied" })[0].tag).toBe("denied");
    const u = withTag("unsupported");
    expect(stepDelivery(u, { tag: "permissionResolved", permission: "granted" })).toEqual([u, []]);
  });

  it("visibilityChanged keeps the permission", () => {
    const [s, cmds] = stepDelivery(granted({ visible: true }), { tag: "visibilityChanged", visible: false });
    expect(s).toMatchObject({ tag: "granted", visible: false });
    expect(cmds).toEqual([]);
  });

  it("a live notice in the background with permission shows exactly the task name and status", () => {
    const [s, cmds] = stepDelivery(granted(), live("n1"));
    expect(cmds).toEqual([{ tag: "showBrowserNotice", id: "n1", title: "Add cart page", body: "Needs you" }]);
    expect(s.delivered.has("n1")).toBe(true);
  });

  it("a live notice is not shown while the tab is in front, without permission, or twice", () => {
    expect(stepDelivery(granted({ visible: true }), live("n1"))[1]).toEqual([]);
    for (const tag of ["unsupported", "default", "denied"] as const) expect(stepDelivery(withTag(tag), live("n1"))[1]).toEqual([]);
    const [s1] = stepDelivery(granted(), live("n1"));
    expect(stepDelivery(s1, live("n1"))).toEqual([s1, []]); // stale: the same ID again
  });

  it("a notice seen in front is remembered, so it does not toast later when the tab is hidden", () => {
    const [s1] = stepDelivery(granted({ visible: true }), live("n1"));
    const [s2] = stepDelivery(s1, { tag: "visibilityChanged", visible: false });
    expect(stepDelivery(s2, live("n1"))[1]).toEqual([]);
  });

  it("history and gap loads never show anything, now or when the same IDs arrive live", () => {
    const [s, cmds] = stepDelivery(granted(), { tag: "historyLoaded", ids: ["h1", "h2"] });
    expect(cmds).toEqual([]);
    expect(stepDelivery(s, live("h1"))[1]).toEqual([]);
  });

  it("a failed delivery leaves the state alone", () => {
    const s = granted();
    expect(stepDelivery(s, { tag: "deliveryFailed", id: "n1" })).toEqual([s, []]);
  });

  it("closed forgets the delivered IDs", () => {
    const [s] = stepDelivery(granted({ delivered: new Set(["a"]) }), { tag: "closed" });
    expect(s.delivered.size).toBe(0);
    expect(s.tag).toBe("granted");
  });
});

// A small seeded generator: no extra dependency, and a failing seed can be replayed.
function rng(seed: number) {
  let x = seed >>> 0 || 1;
  return () => {
    x ^= x << 13;
    x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5;
    x >>>= 0;
    return x / 0x100000000;
  };
}

describe("browser delivery properties", () => {
  it("over random input sequences: nothing is shown twice, history is never shown, permission is asked only after a gesture", () => {
    for (let seed = 1; seed <= 200; seed++) {
      const r = rng(seed);
      const pick = <T,>(xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
      const ids = ["a", "b", "c", "d", "e", "f"];
      let state = initialDelivery();
      const shown: string[] = [];
      const history = new Set<string>();
      for (let i = 0; i < 60; i++) {
        const kind = pick(["cap", "gesture", "resolve", "vis", "live", "live", "hist", "fail", "closed"]);
        let input: DeliveryInput;
        switch (kind) {
          case "cap":
            input = { tag: "capabilityChecked", permission: pick(["unsupported", "default", "denied", "granted"] as const) };
            break;
          case "gesture":
            input = { tag: "permissionGesture" };
            break;
          case "resolve":
            input = { tag: "permissionResolved", permission: pick(["default", "denied", "granted"] as const) };
            break;
          case "vis":
            input = { tag: "visibilityChanged", visible: r() < 0.5 };
            break;
          case "live":
            input = live(pick(ids));
            break;
          case "hist": {
            const some = ids.filter(() => r() < 0.4);
            some.forEach((id) => history.add(id));
            input = { tag: "historyLoaded", ids: some };
            break;
          }
          case "fail":
            input = { tag: "deliveryFailed", id: pick(ids) };
            break;
          default:
            input = { tag: "closed" };
            history.clear();
            shown.length = 0;
        }
        const before = state;
        const [next, cmds] = stepDelivery(state, input);
        for (const c of cmds) {
          if (c.tag === "requestPermission") {
            expect(input.tag, `seed ${String(seed)}`).toBe("permissionGesture");
            expect(before.tag).toBe("default");
          } else {
            expect(input.tag).toBe("liveNotice");
            expect(before.tag).toBe("granted");
            expect(before.visible).toBe(false);
            expect(history.has(c.id), `history ID shown, seed ${String(seed)}`).toBe(false);
            expect(shown.includes(c.id), `shown twice, seed ${String(seed)}`).toBe(false);
            shown.push(c.id);
            expect(Object.keys(c).sort()).toEqual(["body", "id", "tag", "title"]);
          }
        }
        state = next;
      }
    }
  });
});

const n = (cursor: number, over: Partial<NoticeDTO> = {}): NoticeDTO => ({
  id: `n${String(cursor)}`,
  taskId: "t1",
  taskTitle: "Fix cart",
  status: "done",
  revision: cursor,
  createdAt: "2026-01-01T10:00:00Z",
  cursor,
  ...over,
});
const page = (notices: NoticeDTO[], over: Partial<NoticesResponse> = {}): NoticesResponse => ({
  notices,
  oldest: notices.length ? Math.min(...notices.map((x) => x.cursor)) : 0,
  newest: notices.length ? Math.max(...notices.map((x) => x.cursor)) : 0,
  gap: false,
  ...over,
});

describe("notice history merge", () => {
  it("the first page is a history load with no fresh notices, newest first", () => {
    const v = mergeNotices(undefined, page([n(3), n(2), n(1)]));
    expect(v.kind).toBe("initial");
    expect(v.fresh).toEqual([]);
    expect(v.notices.map((x) => x.cursor)).toEqual([3, 2, 1]);
  });

  it("later notices are live and fresh, oldest first, and the list stays newest first", () => {
    const v0 = mergeNotices(undefined, page([n(2), n(1)]));
    const v1 = mergeNotices(v0, page([n(3), n(4)]));
    expect(v1.kind).toBe("live");
    expect(v1.fresh.map((x) => x.cursor)).toEqual([3, 4]);
    expect(v1.notices.map((x) => x.cursor)).toEqual([4, 3, 2, 1]);
    expect(v1.newest).toBe(4);
  });

  it("a page with nothing new is unchanged; a replayed notice is not fresh again", () => {
    const v0 = mergeNotices(undefined, page([n(2), n(1)]));
    expect(mergeNotices(v0, page([])).kind).toBe("unchanged");
    expect(mergeNotices(v0, page([n(2)])).kind).toBe("unchanged");
  });

  it("a gap replaces the list and has no fresh notices (no toasts for history)", () => {
    const v0 = mergeNotices(undefined, page([n(2), n(1)]));
    const v1 = mergeNotices(v0, page([n(9), n(8)], { gap: true }));
    expect(v1.kind).toBe("gap");
    expect(v1.fresh).toEqual([]);
    expect(v1.notices.map((x) => x.cursor)).toEqual([9, 8]);
  });

  it("keeps at most 200", () => {
    const many = Array.from({ length: 260 }, (_, i) => n(i + 1));
    expect(mergeNotices(undefined, page(many)).notices).toHaveLength(200);
  });
});

describe("notice text and unread", () => {
  it("is the task name and a status label, nothing else", () => {
    expect(noticeText({ taskTitle: "Add cart page", status: "verification_failed" })).toEqual({ title: "Add cart page", body: "Checks failed" });
  });
  it("unread is history minus the read IDs", () => {
    expect(unreadIds([n(1), n(2), n(3)] as NoticeView["notices"], new Set(["n2"]))).toEqual(["n1", "n3"]);
  });
});
