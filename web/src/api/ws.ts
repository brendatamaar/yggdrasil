import { MsgError, MsgEvent, MsgGap, MsgSubscribe, MsgUnsubscribe, type WSMessage } from "./gen/api.gen";

export type ConnectionStatus = "connecting" | "open" | "reconnecting" | "closed";

/** The subset of WebSocket the client uses; tests inject a fake. */
export interface SocketLike {
  onopen: (() => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: (() => void) | null;
  send(data: string): void;
  close(): void;
}
export type SocketCtor = new (url: string) => SocketLike;

export interface TopicHandlers {
  onEvent: (msg: WSMessage) => void;
  /** Messages were missed on this topic: refetch its state. */
  onGap: () => void;
}

export interface WSClientOptions {
  url?: string;
  socketCtor?: SocketCtor;
  baseDelayMs?: number;
  maxDelayMs?: number;
}

export function defaultWSUrl(): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/ws`;
}

interface TopicState {
  handlers: Set<TopicHandlers>;
  lastSeq?: number;
}

export class WSClient {
  private readonly url: string;
  private readonly Ctor: SocketCtor;
  private readonly baseDelay: number;
  private readonly maxDelay: number;
  private socket: SocketLike | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private attempt = 0;
  private wantOpen = false;
  private everOpened = false;
  private readonly topics = new Map<string, TopicState>();
  private readonly listeners = new Set<(s: ConnectionStatus) => void>();
  private current: ConnectionStatus = "closed";

  constructor(opts: WSClientOptions = {}) {
    this.url = opts.url ?? defaultWSUrl();
    // Cast: the DOM WebSocket constructor satisfies SocketCtor structurally.
    this.Ctor = opts.socketCtor ?? (WebSocket as unknown as SocketCtor);
    this.baseDelay = opts.baseDelayMs ?? 500;
    this.maxDelay = opts.maxDelayMs ?? 15_000;
  }

  get status(): ConnectionStatus {
    return this.current;
  }

  onStatus(listener: (s: ConnectionStatus) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  connect(): void {
    this.wantOpen = true;
    this.open();
  }

  close(): void {
    this.wantOpen = false;
    clearTimeout(this.timer);
    const s = this.socket;
    this.socket = null;
    if (s) {
      s.onclose = null;
      s.close();
    }
    this.setStatus("closed");
  }

  /** Subscribes to a topic. The server is told on the first handler and on the last removal. */
  subscribe(topic: string, handlers: TopicHandlers): () => void {
    let state = this.topics.get(topic);
    if (!state) {
      state = { handlers: new Set() };
      this.topics.set(topic, state);
      this.send({ type: MsgSubscribe, topic });
    }
    state.handlers.add(handlers);
    return () => {
      const st = this.topics.get(topic);
      if (!st) return;
      st.handlers.delete(handlers);
      if (st.handlers.size === 0) {
        this.topics.delete(topic);
        this.send({ type: MsgUnsubscribe, topic });
      }
    };
  }

  private open(): void {
    if (this.socket) return;
    this.setStatus(this.everOpened ? "reconnecting" : "connecting");
    const s = new this.Ctor(this.url);
    this.socket = s;
    s.onopen = () => {
      const reconnected = this.everOpened;
      this.everOpened = true;
      this.attempt = 0;
      this.setStatus("open");
      for (const [topic, st] of this.topics) {
        st.lastSeq = undefined;
        this.send({ type: MsgSubscribe, topic });
        // Anything sent while we were away is lost.
        if (reconnected) this.emitGap(st);
      }
    };
    s.onmessage = (ev) => {
      this.handle(ev.data);
    };
    s.onclose = () => {
      this.socket = null;
      if (!this.wantOpen) return;
      this.setStatus("reconnecting");
      // shortcut: no jitter; add it if many clients ever reconnect to one server.
      const delay = Math.min(this.maxDelay, this.baseDelay * 2 ** this.attempt);
      this.attempt++;
      this.timer = setTimeout(() => {
        this.open();
      }, delay);
    };
  }

  private handle(raw: unknown): void {
    if (typeof raw !== "string") return;
    let msg: WSMessage;
    try {
      msg = JSON.parse(raw) as WSMessage;
    } catch {
      return;
    }
    const st = msg.topic ? this.topics.get(msg.topic) : undefined;
    if (!st) return;
    if (msg.type === MsgGap) {
      st.lastSeq = undefined;
      this.emitGap(st);
    } else if (msg.type === MsgEvent && msg.seq !== undefined) {
      const expected = st.lastSeq === undefined ? undefined : st.lastSeq + 1;
      st.lastSeq = msg.seq;
      if (expected !== undefined && msg.seq !== expected) this.emitGap(st);
      for (const h of st.handlers) h.onEvent(msg);
    } else if (msg.type !== MsgError) {
      for (const h of st.handlers) h.onEvent(msg);
    }
  }

  private emitGap(st: TopicState): void {
    for (const h of st.handlers) h.onGap();
  }

  private send(msg: WSMessage): void {
    // Only while open; onopen resends every active subscription.
    if (this.current === "open") this.socket?.send(JSON.stringify(msg));
  }

  private setStatus(s: ConnectionStatus): void {
    if (s === this.current) return;
    this.current = s;
    for (const l of this.listeners) l(s);
  }
}

let shared: WSClient | undefined;

/** The app-wide client, created on first use. */
export function getWSClient(): WSClient {
  shared ??= new WSClient();
  return shared;
}
