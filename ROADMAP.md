# Loom roadmap

Loom is an open-source SRE / Platform Engineering agent that runs in your
terminal and never executes an infrastructure action without a policy
verdict. This page says what is shipped, what is being worked on now, what
might come next, and what Loom will not do.

## How we decide

- **Users set the order.** An item in *Next* starts only after at least two
  people independently ask for it in an issue. Upvote (👍) an existing issue
  instead of opening a duplicate.
- **No dates.** Items in *Now* ship in the order listed.
- **Every item has a "done when".** Until it is met, the item is not done.

Have a workflow that is not here? [Open an issue](https://github.com/mordecaiusm922-create/loom/issues/new)
describing what you run today and where it hurts.

## Now

Work in progress. Goal: someone who has never seen Loom installs it and
reaches a first policy verdict on their own, without help.

1. **First run works without guessing.**
   Done when: the README quickstart sets the same API key that `loom init`
   configures, and `loom doctor` reports every configured
   `custom_providers` entry (key present or missing), not only Anthropic.
2. **Redaction test coverage.**
   Done when: tests cover secrets in nested JSON, uncommon token formats,
   multi-line values and error output, and every case is redacted before
   the output reaches the model.
3. **Fuzzing for change classification.**
   Done when: `go test -fuzz` targets exist for argv parsing and cloud/infra
   classification, run in CI for a fixed time budget, and no input makes a
   mutating command classify as read-only.
4. **Feedback issue templates.**
   Done when: the repository offers a "Bug report" template and a
   "This confused me" template, each asking for OS, Loom version, model
   provider and the command that was run.
5. **A plain, status-driven terminal UI.**
   Done when: `loom chat` shows the session state as a colored status
   indicator (ready, investigating, review required, blocked, verified,
   error), and the README screenshots and landing page match it.

## Shipped

- **Governed tool loop.** Every shell command, file write and cloud or
  Kubernetes action is evaluated before it runs: ALLOW, REVIEW, ESCALATE,
  BLOCK or REWRITE. Governance failures fail closed to REVIEW, never ALLOW.
- **Built-in local policy, no account needed.** `governance.engine: auto`
  uses the local policy (reads run, changes ask, catastrophic commands are
  blocked) unless a DevMind token is configured.
- **Infrastructure-aware routing.** Destructive Terraform, kubectl and Helm
  commands are classified from argv and sent to the change engine, so
  blast-radius rules apply even when the command goes through the shell.
- **Native read-only tools.** `k8s_get`, `k8s_describe`, `k8s_logs`,
  `cloud_read` and `metrics_query`, with condensed output for common
  resource types and `raw: true` for the full payload.
- **Allowlisted cloud reads.** `cloud_read` runs only recognized read
  operations for aws, gcloud and az, refuses reads that return secrets, and
  refuses anything it does not recognize.
- **Credential protection.** `.env` files, Terraform state, cloud
  credential directories and kubeconfig are never read or written, and
  tool output is redacted (best effort) before it reaches the model.
  `loom init` writes the config file with owner-only permissions.
- **Sessions you can audit.** One `.jsonl` timeline per session with the
  decision, the outcome and the governance audit id; `--resume` continues
  a previous session from its summary.
- **Runbooks.** JSON runbooks as structured starting points
  (`loom run --runbook pod-crashloop "..."`).
- **Dry run and non-interactive mode.** REVIEW and ESCALATE are never
  auto-approved without a human.
- **MCP servers as tools**, under the same governance as built-in tools.
- **GitHub Actions example** for running Loom in CI.
- **Transient failure tolerance.** Governance cold starts (connection
  errors, HTTP 502/503/504) and overloaded model APIs are retried with
  backoff before failing.
- **Cross-platform releases** for Linux, macOS and Windows (amd64/arm64).

## Next: candidates, started only on demand

Each item states the problem it solves. None is committed until the demand
rule above is met.

- **plan → policy → apply for Terraform/OpenTofu.**
  Problem: an approved change can drift between review and apply.
  Done when: Loom runs `terraform plan -out`, shows a per-resource risk
  table (stateful replacements flagged), evaluates the plan JSON against
  policy, and `loom apply` applies only that reviewed plan, verified by
  checksum.
- **Kubernetes diffs before apply.**
  Problem: a manifest change is reviewed without seeing what it changes.
  Done when: every apply is preceded by `kubectl diff` and a server-side
  dry run, shown with the same per-resource risk table.
- **Approvals outside the terminal.**
  Problem: the on-call engineer is not always the person who can approve.
  Done when: a REVIEW can be approved or rejected by another person from
  Slack, with a timeout that fails closed.
- **Project context file (`LOOM.md`).**
  Problem: every session re-explains environment names and ownership.
  Done when: Loom reads `LOOM.md` at session start and the model can cite
  it.
- **Plan comments on pull requests.**
  Problem: reviewers do not see the policy verdict where they review.
  Done when: in CI, Loom posts one comment per pull request with the
  verdict for each changed resource and updates it on new pushes.

## Later

Larger efforts for teams, considered only after *Next* items have real
users.

- Organization-wide policies shared across projects.
- Server-side audit storage, in addition to (never instead of) the local
  session log.
- Fleet visibility: which agents ran what, where, and with which outcome.
- SSO and role-based access per task type and environment.

## Not planned

- **A general-purpose coding agent.** Loom stays within infrastructure and
  platform operations.
- **Auto-approving REVIEW or ESCALATE.** A human always decides;
  `--non-interactive` fails them closed instead of approving them.
- **An LLM in the decision path.** The model proposes; a deterministic
  policy decides.
- **Telemetry.** Loom only talks to the model provider, the policy engine
  and the observability endpoints you configure.
