# Loom roadmap

Loom is an open-source SRE / Platform Engineering agent that runs in your
terminal and never executes an infrastructure action without a policy
verdict. This roadmap lists what is shipped and what comes next. Dates are
intentionally left out; the order is the order we expect to work in.

## Shipped

- **Governed tool loop.** Every shell command, file write and cloud or
  Kubernetes action is evaluated before it runs: ALLOW, REVIEW, ESCALATE,
  BLOCK or REWRITE. Governance failures fail closed to REVIEW, never ALLOW.
- **Infrastructure-aware routing.** Destructive Terraform, kubectl and Helm
  commands are classified from argv and sent to the change engine, so
  blast-radius rules apply even when the command goes through the shell.
- **Native read-only tools.** `k8s_get`, `k8s_describe`, `k8s_logs`,
  `cloud_read` and `metrics_query`, with condensed output for common
  resource types and `raw: true` for the full payload.
- **Credential protection.** `.env` files, Terraform state, cloud
  credential directories and kubeconfig are never read or written, and
  tool output is redacted before it reaches the model.
- **Sessions you can audit.** One `.jsonl` timeline per session with the
  decision, the outcome and the governance audit id; `--resume` continues
  a previous session from its summary.
- **Runbooks.** JSON runbooks as structured starting points
  (`loom run --runbook pod-crashloop "..."`).
- **Dry run and non-interactive mode.** REVIEW and ESCALATE are never
  auto-approved without a human.
- **MCP servers as tools**, under the same governance as built-in tools.
- **GitHub Actions example** for running Loom in CI.
- **Cold-start tolerance.** Transient governance failures (connection
  errors, HTTP 502/503/504) are retried with backoff before failing closed.
- **Cross-platform releases** for Linux, macOS and Windows (amd64/arm64).

## Next

1. **plan → policy → apply for Terraform/OpenTofu.** `terraform plan -out`,
   a per-resource risk table (including replacements of stateful
   resources), policy evaluation of the plan JSON, and `loom apply` that
   only applies the reviewed plan, verified by checksum.
2. **Kubernetes diffs.** `kubectl diff` plus a server-side dry run before
   any apply, with the same per-resource risk table.
3. **Approvals outside the terminal.** A REVIEW can be approved by another
   person from Slack, with a timeout that fails closed.
4. **Project context file** (`LOOM.md`) for environment names, ownership
   and conventions.
5. **Plan comments on pull requests** with the verdict for each resource.

## Later

- Organization-wide policies shared across projects.
- Server-side audit storage, in addition to (never instead of) the local
  session log.
- Fleet visibility: which agents ran what, where, and with which outcome.
- SSO/RBAC per task type and environment.

Have a use case that is not here? Open an issue describing the workflow.
