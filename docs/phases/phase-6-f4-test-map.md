# F4 review repair regression map

PRD AC-01/05, NR-02, SF-05; approved action/new-repo persistence designs.
Owner: test-engineer, tests only in internal/db after lead production handoff.
No parallel lead production changes while DB test ownership is active.

| Contract / defect | Required evidence |
|---|---|
| Begin accepts only status/refusal/key combinations that can load | Table across every persisted ActionState, known/empty/unknown refusal and present/empty key. Reject non-refused + nonempty reason, refused + invalid reason and submitting + missing key before insertion. |
| Invalid begin rolls back caller work | Real SQLite transaction appends fictional audit or other fixture row before rejected begin; propagate error and assert no request or collateral row survives. Existing unrelated unsettled request still loads. |
| Every successful begin can reload | Accepted combinations round-trip via GetActionRequest, duplicate Begin and applicable unsettled list; full immutable request and digest retained. Exercise valid save transitions and rejection parity too. |
| Independent decoder validity | Replace accepted-implies-valid oracle that calls Request.Validate/ParseRequest with a test-only independent predicate of documented ID/kind/source/agent/model and state/refusal/key rules. Cover nullable identity, malformed fields, digest mismatch and full reconstructed identity. |
| Rules are enforced rather than mirrored | Show each new rejection assertion can fail by controlled removal of insert guard or test negative control; independently weakening a request rule must be caught by the independent validity predicate. Restore every mutation exactly. |

Keep migration/restart/contender tests intact. Do not change production, schema,
queries, generated files or other packages. Use temp SQLite and fictional data.
Run meaningful scoped repeated/fuzz/race checks plus the five mandatory tasks
through the shared validation helper, preserve failed logs, report skips. Lead
merges sequentially and repeats both independent reviews after all repairs land.
