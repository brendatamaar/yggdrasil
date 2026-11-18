import { useEffect, useMemo, useState } from "react";
import type { NewRepoOperationDTO, ProposalDTO, PublishPreviewResponse } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { ProposalsByIds } from "../proposals";
import * as api from "./api";
import type { PublishForm } from "./core";
import { useHostInstances, usePrepareMachine } from "./hooks";
import { useSpecMemory } from "./store";
import { btn, Field, Footer, Guidance, Hash, input, Lines, primary, Problems } from "./ui";

/**
 * Publish a ready repo (NR-03, NR-04): choose a host account, see exactly what would be created
 * (private unless you pick public), then create the pending publish proposal. The remote is made
 * only after that proposal is confirmed in the Review panel below it.
 */
export function PublishPanel({ operation, onClose }: { operation: NewRepoOperationDTO; onClose: () => void }) {
  const hosts = useHostInstances(true);
  const remember = useSpecMemory((s) => s.remember);
  const machine = usePrepareMachine<PublishForm, PublishPreviewResponse, ProposalDTO>({
    queryPreview: (f, signal) => api.previewPublish(operation.operationId, f, signal),
    send: (f, hash, requestId) => api.publishNewRepo(operation.operationId, { ...f, requestId, hash }),
  });
  const { state } = machine;

  // The person confirmed this exact spec by creating the proposal; keep it for an explicit retry.
  useEffect(() => {
    if (state.tag === "created") remember(operation.operationId, { kind: "publish", hash: state.preview.hash, lines: state.preview.lines });
  }, [state, operation.operationId, remember]);

  const instances = useMemo(() => hosts.data?.instances ?? [], [hosts.data]);
  const usable = instances.filter((i) => i.tokenConfigured);
  const [form, setForm] = useState<PublishForm>({ hostInstance: "", owner: "", ownerKind: "user", name: operation.leaf, visibility: "private" });
  const host = form.hostInstance || usable[0]?.id || "";
  const set = (patch: Partial<PublishForm>) => {
    setForm((f) => ({ ...f, ...patch }));
  };

  if (hosts.isPending) return <p role="status" className="text-sm text-muted">Looking for configured hosts…</p>;
  if (hosts.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {`Couldn't read the configured hosts. ${describeError(hosts.error)}`}
      </p>
    );
  }
  if (instances.length === 0) {
    return (
      <Guidance kind="NoRemote">
        No git host is set up, so there is nowhere to publish this repo yet. Add a host in Settings, then come back. The repo stays on your disk until then.
      </Guidance>
    );
  }
  if (usable.length === 0) {
    return (
      <Guidance kind="NoCredentials">
        A host is set up but has no token, so Yggdrasil can't create a repository on it. Add a token for the host in Settings. Local features still work.
      </Guidance>
    );
  }

  const ready = form.owner.trim() !== "" && form.name.trim() !== "";
  const publishForm = (): PublishForm => ({ ...form, hostInstance: host, owner: form.owner.trim(), name: form.name.trim() });

  switch (state.tag) {
    case "editing":
      return (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) machine.preview(publishForm());
          }}
        >
          <h3 className="text-sm font-semibold">Publish to a host</h3>
          <Field label="Host">
            <select className={input} value={host} onChange={(e) => { set({ hostInstance: e.target.value }); }}>
              {usable.map((i) => (
                <option key={i.id} value={i.id}>
                  {i.id}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Owner (account or organization)">
            <input className={input} value={form.owner} onChange={(e) => { set({ owner: e.target.value }); }} />
          </Field>
          <fieldset className="flex gap-4 text-sm">
            <legend className="font-medium">Owner type</legend>
            {(["user", "org"] as const).map((k) => (
              <label key={k} className="flex items-center gap-1">
                <input type="radio" name="owner-kind" checked={form.ownerKind === k} onChange={() => { set({ ownerKind: k }); }} />
                {k === "user" ? "Personal account" : "Organization"}
              </label>
            ))}
          </fieldset>
          <Field label="Repository name">
            <input className={input} value={form.name} onChange={(e) => { set({ name: e.target.value }); }} />
          </Field>
          <fieldset className="space-y-1 text-sm">
            <legend className="font-medium">Who can see it</legend>
            <label className="flex items-center gap-1">
              <input type="radio" name="visibility" checked={form.visibility === "private"} onChange={() => { set({ visibility: "private" }); }} />
              Private (default)
            </label>
            <label className="flex items-center gap-1">
              <input type="radio" name="visibility" checked={form.visibility === "public"} onChange={() => { set({ visibility: "public" }); }} />
              Public: anyone can see it
            </label>
          </fieldset>
          <Footer>
            <button type="button" className={btn} onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className={primary} disabled={!ready}>
              Preview
            </button>
          </Footer>
        </form>
      );
    case "previewing":
      return <p role="status" className="text-sm text-muted">Preparing the preview…</p>;
    case "previewed": {
      const p = state.preview;
      const blocked = (p.problems?.length ?? 0) > 0;
      return (
        <div className="space-y-3">
          <h3 className="text-sm font-semibold">What will be created</h3>
          <p className="text-sm">
            Visibility: <strong>{p.visibility === "public" ? "Public (anyone can see it)" : "Private"}</strong>
          </p>
          <Lines label="What will be created" lines={p.lines} />
          <Hash hash={p.hash} />
          <Problems problems={p.problems} />
          <p className="text-xs text-muted">This only creates a proposal. Nothing is created on the host until you confirm it in Review.</p>
          <Footer>
            <button type="button" className={btn} onClick={machine.edit}>
              Back
            </button>
            <button type="button" className={primary} disabled={blocked} onClick={machine.submit}>
              Create publish proposal
            </button>
          </Footer>
        </div>
      );
    }
    case "submitting":
      return <p role="status" className="text-sm text-muted">Creating the proposal… It is sent once.</p>;
    case "created":
      return (
        <div className="space-y-2">
          <p className="text-sm">The publish proposal is ready. Review it and confirm to create the remote repository.</p>
          <ProposalsByIds ids={[state.result.id]} />
        </div>
      );
    case "failed":
      return (
        <div className="space-y-2">
          <p role="alert" className="text-sm text-danger">
            {state.message}
          </p>
          {state.retry.tag === "submit" ? (
            <p className="text-xs text-muted">The proposal may or may not have been created. Trying again sends the same request, so it cannot be created twice.</p>
          ) : null}
          <Footer>
            <button type="button" className={btn} onClick={machine.edit}>
              Edit
            </button>
            <button type="button" className={primary} onClick={machine.retry}>
              Try again
            </button>
          </Footer>
        </div>
      );
  }
}
