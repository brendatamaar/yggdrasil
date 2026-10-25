package guard

// Reason is a stable code for why a call or a turn was refused. Message gives
// the plain-language text that the model, the transcript and the UI see. A
// message never carries a path or any text of the call (invariant G8).
type Reason string

const (
	// Reasons of Decide (D1 to D15).
	ReasonUnknownTool          Reason = "unknown_tool"
	ReasonPathUnresolved       Reason = "path_unresolved"
	ReasonOutsideRepo          Reason = "outside_repo"
	ReasonAskIsReadOnly        Reason = "ask_is_read_only"
	ReasonProtectedPath        Reason = "protected_path"
	ReasonOutsideWorktree      Reason = "outside_worktree"
	ReasonShellSyntax          Reason = "shell_syntax"
	ReasonNotAllowlisted       Reason = "not_allowlisted"
	ReasonAgentWorktreeBlocked Reason = "agent_worktree_blocked"
	// ReasonPolicyUnavailable: the hook could not load the task's rules.
	ReasonPolicyUnavailable Reason = "policy_unavailable"

	// Reasons of Admit.
	ReasonRepoUntrusted        Reason = "repo_untrusted"
	ReasonNeedsOptIn           Reason = "needs_opt_in"
	ReasonLimitedEnforcement   Reason = "limited_enforcement"
	ReasonModeSwitchNotAllowed Reason = "mode_switch_not_allowed"
	// ReasonModeUnavailable is what a stub Admit says for every mode but Ask.
	ReasonModeUnavailable Reason = "mode_unavailable"
)

var reasonMessages = map[Reason]string{
	ReasonUnknownTool:          "That tool isn't allowed in this task.",
	ReasonPathUnresolved:       "That path couldn't be checked, so it is blocked.",
	ReasonOutsideRepo:          "That is outside the folders this task may read.",
	ReasonAskIsReadOnly:        "This task is in Ask mode, which is read-only.",
	ReasonProtectedPath:        "That file is protected. Propose the change instead and the user will confirm it.",
	ReasonOutsideWorktree:      "That is outside the folders this task may change.",
	ReasonShellSyntax:          "Run one plain command at a time, without pipes, redirects or chained commands.",
	ReasonNotAllowlisted:       "That command isn't on the list this task may run.",
	ReasonAgentWorktreeBlocked: "Sub-agents can't use their own worktree here.",
	ReasonPolicyUnavailable:    "The task's rules could not be loaded, so the call is blocked.",
	ReasonRepoUntrusted:        "This repo ships its own agent settings. Review and trust them first; until then only Ask mode is available.",
	ReasonNeedsOptIn:           "This mode edits your main checkout directly. Turn on the opt-in to use it.",
	ReasonLimitedEnforcement:   "This agent can't be restricted tightly enough for this mode.",
	ReasonModeSwitchNotAllowed: "A task can switch only between Ask and the mode it started in.",
	ReasonModeUnavailable:      "That mode isn't available yet.",
}

// Message is the plain-language text for r. An unknown code gets the generic
// blocked message, never an empty one.
func (r Reason) Message() string {
	if m, ok := reasonMessages[r]; ok {
		return m
	}
	return "That call is blocked."
}

// Verdict is the answer of Decide.
type Verdict struct {
	Allow bool
	// Reason is set when Allow is false.
	Reason Reason
}

// Deny returns a denying verdict.
func Deny(r Reason) Verdict { return Verdict{Reason: r} }

// Allowed is the allowing verdict.
func Allowed() Verdict { return Verdict{Allow: true} }
