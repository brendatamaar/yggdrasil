import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useState } from "react";
import type { NewRepoOperationDTO, NewRepoPreviewResponse } from "../../api/gen/api.gen";
import * as api from "./api";
import { describeOperation, emptyInitForm, initRequest, leafProblem, type InitForm } from "./core";
import { usePrepareMachine, useProjectOperations } from "./hooks";
import { OperationView } from "./OperationView";
import { useSpecMemory } from "./store";
import { btn, Field, Footer, Hash, input, Lines, primary, Problems } from "./ui";

/**
 * New repo (NR-01, NR-02): name a folder, preview exactly what will be created, reserve the name,
 * then confirm the initialization through the Review panel. A preview creates nothing; a reservation
 * creates no folder; only that confirmation does.
 */
export function NewRepoDialog({
  projectId,
  projectName,
  open,
  onOpenChange,
}: {
  projectId: string;
  projectName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content
          className="fixed top-1/2 left-1/2 max-h-[90vh] w-[40rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 overflow-auto rounded-md border-2 border-line bg-canvas p-4 text-fg shadow-lg"
          aria-describedby="newrepo-desc"
        >
          <Dialog.Title className="text-base font-semibold">New repo in {projectName}</Dialog.Title>
          <Dialog.Description id="newrepo-desc" className="text-sm text-muted">
            Preview first. Nothing is created on disk until you confirm.
          </Dialog.Description>
          {/* Mounted only while open, so closing resets the form. */}
          <Body projectId={projectId} onClose={() => { onOpenChange(false); }} />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Body({ projectId, onClose }: { projectId: string; onClose: () => void }) {
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const [form, setForm] = useState<InitForm>(emptyInitForm());
  const remember = useSpecMemory((s) => s.remember);
  const ops = useProjectOperations(projectId, true);
  const machine = usePrepareMachine<InitForm, NewRepoPreviewResponse, NewRepoOperationDTO>({
    queryPreview: (f, signal) => api.previewNewRepo(projectId, initRequest(f), signal),
    send: (f, hash, requestId) => api.reserveNewRepo(projectId, { ...initRequest(f), requestId, hash }),
  });
  const { state } = machine;

  // The person confirmed this exact spec by reserving; keep it so an explicit Retry can confirm it again.
  useEffect(() => {
    if (state.tag === "created") remember(state.result.operationId, { kind: "init", hash: state.preview.hash, lines: state.preview.lines });
  }, [state, remember]);

  const opId = selected ?? (state.tag === "created" ? state.result.operationId : undefined);
  if (opId) {
    return (
      <OperationView
        operationId={opId}
        onBack={() => {
          setSelected(undefined);
          machine.reset();
        }}
      />
    );
  }

  const set = (patch: Partial<InitForm>) => {
    setForm((f) => ({ ...f, ...patch }));
  };

  switch (state.tag) {
    case "editing": {
      const problem = form.leaf === "" ? undefined : leafProblem(form.leaf);
      const existing = ops.data ?? [];
      return (
        <div className="space-y-3">
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (!leafProblem(form.leaf)) machine.preview(form);
            }}
          >
            <Field label="Folder name" hint="One new folder inside this project. It must not exist yet.">
              <input className={input} value={form.leaf} autoFocus onChange={(e) => { set({ leaf: e.target.value }); }} />
            </Field>
            {problem ? (
              <p role="alert" className="text-sm text-danger">
                {problem}
              </p>
            ) : null}
            <details className="space-y-2 text-sm">
              <summary className="cursor-pointer text-muted">Change the starting files (optional)</summary>
              <Field label="README" hint="Leave empty for the default.">
                <textarea className={`${input} font-mono`} rows={3} value={form.readme} onChange={(e) => { set({ readme: e.target.value }); }} />
              </Field>
              <Field label="Ignore file" hint="Leave empty for the default.">
                <textarea className={`${input} font-mono`} rows={3} value={form.ignore} onChange={(e) => { set({ ignore: e.target.value }); }} />
              </Field>
              <Field label="First commit message" hint="Leave empty for the default.">
                <input className={input} value={form.commitMessage} onChange={(e) => { set({ commitMessage: e.target.value }); }} />
              </Field>
            </details>
            <Footer>
              <button type="button" className={btn} onClick={onClose}>
                Cancel
              </button>
              <button type="submit" className={primary} disabled={form.leaf.trim() === "" || leafProblem(form.leaf) !== undefined}>
                Preview
              </button>
            </Footer>
          </form>
          {ops.isError ? (
            <p role="alert" className="text-sm text-danger">
              Couldn't read earlier setups. Try again later.
            </p>
          ) : existing.length > 0 ? (
            <section aria-label="Earlier setups" className="space-y-1 border-t border-line pt-2">
              <h3 className="text-sm font-semibold">Earlier setups in this project</h3>
              <ul className="space-y-1 text-sm">
                {existing.map((o) => (
                  <li key={o.operationId} className="flex items-center justify-between gap-2">
                    <span>
                      {o.leaf} <span className="text-muted">({describeOperation(o).headline})</span>
                    </span>
                    <button
                      type="button"
                      className={btn}
                      onClick={() => {
                        setSelected(o.operationId);
                      }}
                    >
                      Open {o.leaf}
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </div>
      );
    }
    case "previewing":
      return (
        <>
          <p role="status" className="text-sm text-muted">
            Preparing the preview…
          </p>
          <Footer>
            <button type="button" className={btn} onClick={machine.reset}>
              Cancel
            </button>
          </Footer>
        </>
      );
    case "previewed": {
      const p = state.preview;
      const blocked = (p.problems?.length ?? 0) > 0;
      return (
        <div className="space-y-3">
          <dl className="grid grid-cols-[8rem_1fr] gap-1 text-sm">
            <dt className="text-muted">Folder</dt>
            <dd className="font-mono">{p.spec.leaf}</dd>
            <dt className="text-muted">Branch</dt>
            <dd className="font-mono">{p.spec.branch}</dd>
            <dt className="text-muted">First commit</dt>
            <dd>{p.spec.commitMessage}</dd>
          </dl>
          <Lines label="What will happen" lines={p.lines} />
          <Hash hash={p.hash} />
          <Problems problems={p.problems} />
          <p className="text-xs text-muted">Reserving the name creates no folder. You confirm the setup in the next step.</p>
          <Footer>
            <button type="button" className={btn} onClick={machine.edit}>
              Back
            </button>
            <button type="button" className={primary} disabled={blocked} onClick={machine.submit}>
              Reserve and review
            </button>
          </Footer>
        </div>
      );
    }
    case "submitting":
      return (
        <p role="status" className="text-sm text-muted">
          Reserving the name… It is sent once.
        </p>
      );
    case "created":
      return null;
    case "failed":
      return (
        <div className="space-y-2">
          <p role="alert" className="text-sm text-danger">
            {state.message}
          </p>
          {state.retry.tag === "submit" ? (
            <p className="text-xs text-muted">
              The name may or may not have been reserved. Trying again sends the same request, so it cannot be reserved twice.
            </p>
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
