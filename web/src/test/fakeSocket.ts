import type { SocketLike } from "../api/ws";

/** Records what the client sends; tests drive it with open(), receive() and drop(). */
export class FakeSocket implements SocketLike {
  static instances: FakeSocket[] = [];
  static last(): FakeSocket {
    const s = FakeSocket.instances.at(-1);
    if (!s) throw new Error("no socket created");
    return s;
  }

  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  sent: unknown[] = [];

  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data));
  }

  close(): void {
    this.onclose?.();
  }

  open(): void {
    this.onopen?.();
  }

  receive(msg: unknown): void {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }

  /** Server side goes away. */
  drop(): void {
    this.onclose?.();
  }
}
