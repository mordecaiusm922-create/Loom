# Grantline roadmap

Grantline (formerly Loom) is the open-source harness SRE, DevOps, Platform
Engineering and Cloud Security teams use to deploy AI agents on their
infrastructure. No agent executes an infrastructure action without a policy
verdict, and the project is heading towards plan-scoped cloud access: the
reviewed plan becomes the credential. This page says what is shipped, what
is being worked on now, the bet being validated, what might come next, and
what Grantline will not do.

## How we decide

- **Users set the order.** An item in *Next* starts only after at least two
  people independently ask for it in an issue. Upvote (👍) an existing issue
  instead of opening a duplicate.
- **No dates.** Items in *Now* ship in the order listed.
- **Every item has a "done when".** Until it is met, the item is not done.

Have a workflow that is not here? [Open an issue](https://github.com/mordecaiusm922-create/grantline/issues/new)
describing what you run today and where it hurts.

## Now

Work in progress, in order. Goal: a team installs Grantline on its own,
reaches a first policy verdict, and then deploys its first agent.

1. **First run works without guessing.**
   Done when: the README quickstart sets the same API key that `grantline init`
   configures, and `grantline doctor` reports every configured
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
   "This confused me" template, each asking for OS, Grantline version, model
   provider and the command that was run.
5. **A terminal UI that reads like a change plan.**
   Done when: proposed actions are listed the way `terraform plan` lists
   changes (one line per action with its verdict, risk and rule), approval
   takes an explicit `yes`, and the README screenshots and landing page
   match it.
6. **Agent specs.** Teams define their own agents in `agents/<name>.yaml`
   (role, workflow, trigger, environments, tools, policy, output).
   Done when: `grantline agents new|validate|ls|run` work; a spec can only
   narrow the tools, environments and policy of the base configuration
   (anything else is rejected); prod is reachable only when listed
   explicitly; and an unattended agent never approves a REVIEW, it leaves
   the plan pending for a person.
7. **The change workflow.** plan → review → apply, the first workflow an
   agent can run.
   Done when: the plan lists one line per resource with its verdict, risk
   and IAM findings (wildcard actions or resources, `iam:PassRole` without a
   service condition, public exposure); it is saved with its sha256; and
   `grantline apply` refuses a plan whose checksum changed.
8. **Deploying agents to GitHub Actions.**
   Done when: `grantline deploy agents/<name>.yaml --target github-actions`
   writes a workflow the team reviews and commits, triggered by pull
   requests or a schedule, that posts the plan as a pull request comment
   and only references secrets through `${{ secrets.* }}`.

## The bet: plan-scoped cloud credentials (validating)

Problem: agents apply changes with standing cloud credentials. A policy
verdict decides what the agent is allowed to run, but nothing in the cloud
stops an agent that drifts from what was approved.

Idea: the reviewed plan becomes the credential. `grantline apply` assumes a
role with a short-lived session whose session policy is derived from the
plan, so AWS itself refuses anything outside it.

Status: being validated with SRE and cloud security teams. Building starts
when at least two teams confirm they would run it on a real account.

Done when (first version: AWS and Terraform/OpenTofu):
- `grantline plan` saves the plan with its sha256 and shows a per-resource
  risk table, including IAM changes (wildcard actions or resources,
  `iam:PassRole` without a service condition, public exposure).
- `grantline apply` refuses a plan whose checksum changed, assumes a role
  with a session policy limited to the plan's actions and resources and a
  lifetime of 15 minutes or less, and applies only that plan.
- Every apply writes an evidence record whose STS session name can be
  joined with CloudTrail.

Known limits: values only known after apply (generated ARNs) need a
documented widening rule; session policies have size limits; GCP and Azure
come later.

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
  `grantline init` writes the config file with owner-only permissions.
  The session audit trail (`.grantline/`) is also off-limits to the file
  tools, so the model cannot read it back or rewrite it.
- **Sessions you can audit.** One `.jsonl` timeline per session with the
  decision, the outcome and the governance audit id; `--resume` continues
  a previous session from its summary.
- **Runbooks.** JSON runbooks as structured starting points
  (`grantline run --runbook pod-crashloop "..."`).
- **Dry run and non-interactive mode.** REVIEW and ESCALATE are never
  auto-approved without a human.
- **MCP servers as tools**, under the same governance as built-in tools.
- **GitHub Actions example** for running Grantline in CI.
- **Transient failure tolerance.** Governance cold starts (connection
  errors, HTTP 502/503/504) and overloaded model APIs are retried with
  backoff before failing.
- **Cross-platform releases** for Linux, macOS and Windows (amd64/arm64).

## Next: candidates, started only on demand

Each item states the problem it solves. None is committed until the demand
rule above is met.

- **Kubernetes diffs before apply.**
  Problem: a manifest change is reviewed without seeing what it changes.
  Done when: every apply is preceded by `kubectl diff` and a server-side
  dry run, shown with the same per-resource risk table.
- **Approvals outside the terminal.**
  Problem: the on-call engineer is not always the person who can approve.
  Done when: a REVIEW can be approved or rejected by another person from
  Slack, with a timeout that fails closed.
- **Project context file (`GRANTLINE.md`).**
  Problem: every session re-explains environment names and ownership.
  Done when: Grantline reads `GRANTLINE.md` at session start and the model can cite
  it.
- **Plan-scoped credentials for GCP and Azure.**
  Problem: the first version covers AWS only.
  Done when: the same apply flow mints scoped, short-lived credentials on
  GCP and Azure, with the limits of each provider documented.
- **Workspace awareness.**
  Problem: teams describe by hand what their repository already says.
  Done when: `grantline init` detects Terraform/OpenTofu, Terragrunt, Helm,
  Kustomize, Argo CD/Flux, CI workflows, runbooks, kube contexts and cloud
  profiles, proposes environments with their tier, and writes nothing
  without confirmation.
- **The audit workflow (Cloud Security).**
  Problem: finding risky IAM and public exposure means stitching together
  several tools.
  Done when: a read-only agent produces a findings report (Markdown and
  JSON) with severity and evidence, and cannot run a mutating action.
- **The incident workflow (SRE on-call).**
  Problem: the timeline and the postmortem are rebuilt by hand after every
  incident.
  Done when: an agent runs triage from a symptom or an alert, proposes
  mitigations through REVIEW, and writes a timeline and postmortem draft
  from the session log.
- **Deploying agents to Kubernetes.**
  Problem: some agents must run next to the cluster or react to alerts.
  Done when: `grantline deploy --target k8s` writes a CronJob or Deployment
  with a minimal ServiceAccount and RBAC, and agents can be triggered by an
  Alertmanager webhook.

## Later

Larger efforts for teams, considered only after *Next* items have real
users.

- Organization-wide policies shared across projects.
- Server-side audit storage, in addition to (never instead of) the local
  session log.
- Fleet visibility: which agents ran what, where, and with which outcome.
- SSO and role-based access per task type and environment.

## Not planned

- **A general-purpose coding agent.** Grantline stays within infrastructure and
  platform operations.
- **Auto-approving REVIEW or ESCALATE.** A human always decides;
  `--non-interactive` fails them closed instead of approving them.
- **An LLM in the decision path.** The model proposes; a deterministic
  policy decides.
- **Telemetry.** Grantline only talks to the model provider, the policy engine
  and the observability endpoints you configure.
