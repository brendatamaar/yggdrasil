import type { NoiseCandidateDTO } from "../../api/gen/api.gen";

/** What the suggestion is based on, in words. The numbers come from a sample, and the text says so. */
export function evidenceText(c: NoiseCandidateDTO): string {
  const e = c.evidence;
  const base = `${String(e.underDirectory)} of ${String(e.sampled)} sampled files are under ${c.directory} (${String(e.total)} untracked in all).`;
  return c.incomplete ? `${base} Based on a sample.` : base;
}
