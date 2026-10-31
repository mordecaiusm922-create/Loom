# Grantline

[![ci](https://github.com/mordecaiusm922-create/grantline/actions/workflows/ci.yml/badge.svg)](https://github.com/mordecaiusm922-create/grantline/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**Deploy AI agents into your infrastructure workflows, and they only touch what you approved.**

> Grantline was called **Loom** until v0.5.x. Existing `loom.config.json` files and `.loom/` sessions are still read; rename them when convenient.

Grantline is the open-source harness SRE, DevOps, Platform Engineering and
Cloud Security teams use to run AI agents on their infrastructure. The harness
is everything around the model: infrastructure-native tools, the context of
your environments, a deterministic policy gate on every action, and an audit
trail. The model is replaceable; the harness is the product. It is not a
general-purpose coding agent.

Today you run Grantline from your terminal or a CI job: it investigates with
read-only tools, proposes changes, and runs nothing until the policy returns
a verdict.

**Where it is going** ([roadmap](ROADMAP.md)), not available yet: each team
defines its own agents in a file and deploys them where it works (terminal,
GitHub Actions, Kubernetes). A spec can only narrow what an agent may do,
and an unattended agent never approves its own changes.

```yaml
# agents/tf-change.yaml (planned format)
name: tf-change
role: devops                 # sre | devops | platform | cloudsec
workflow: change             # plan -> review -> apply
trigger: pull_request
environments: [staging]      # prod only when listed explicitly
tools: [cloud_read, k8s_get, terraform]
policy:
  review_in: [prod]          # adds restrictions, never relaxes the base policy
output: [plan_file, pr_comment]
```

Later, the reviewed plan itself becomes the credential: an apply runs with
short-lived cloud credentials limited to that plan, enforced by the cloud
provider rather than by the agent.

## Try it in two minutes

```bash
go install github.com/mordecaiusm922-create/grantline/cmd/grantline@latest  # or grab a binary from Releases
grantline init                       # writes grantline.config.json (owner-only permissions)
export ANTHROPIC_API_KEY=sk-ant-...  # the config from `grantline init` uses Anthropic; for OpenAI, Gemini,
                                    # Groq or a local Ollama model, see Configuration below
grantline doctor                     # checks your shell, kubectl/terraform and the policy engine
grantline chat                       # ask it to investigate something; every change asks you first
```

No account needed: the built-in local policy blocks catastrophic commands and asks before any change. Tried it? [Open an issue](https://github.com/mordecaiusm922-create/grantline/issues/new) — even "this confused me" helps.

Grantline is a governed agent harness for SRE, DevOps, Platform Engineering and Cloud Security.

It is not a general-purpose coding agent. Its domain is infrastructure and
platform operations: Terraform/OpenTofu, Kubernetes/Helm, cloud CLIs
(aws/gcloud/az), CI/CD, observability (Prometheus/Grafana/Datadog), incident
response, capacity, schema migrations, and access control (IAM, secret
rotation). The system prompt enforces this scope: asked for unrelated
application feature work, Grantline says so and redirects instead of improvising
outside its domain.

The model thinks, Grantline acts, and a policy gate decides whether each tool
action may run: a conservative local policy out of the box, or DevMind, a
deterministic policy engine where the same action always gets the same
decision and no LLM sits in the decision path. The model provider is
replaceable; the policy gate is never optional.

The shell underneath Grantline is configurable with `sre.shell`. When unset, it
follows the host OS:

- `"bash"` (default on Linux and macOS) — commands run as
  `bash --noprofile --norc -o pipefail -c`, so dotfiles can't change
  behavior and a failure inside a pipeline is reported, not masked.
- `"powershell"` (default on Windows) — PowerShell 7+ (`pwsh`), also usable
  on Linux and macOS by setting it explicitly.

Only the configured shell is offered to the model (as a tool named
`powershell` or `bash`), so it never writes bash syntax into PowerShell or
the reverse. Both go through the same infra classification and governance.

## What works

- Per-request model routing to Anthropic, Ollama, or any configured
  OpenAI-compatible API. Use `--task` for a deterministic route in CI
  (`trivial_edit`, `planning`, `security_review`, `incident_response`).
- Native tool-call conversations for Anthropic, modern Ollama models, and
  OpenAI-compatible providers that declare `supports_tooling: true`.
- A DevMind decision before **every** built-in tool call — the generic
  shell escape hatch (`bash` or `powershell`), `read_file`, `write_file`, `update_plan`, and
  every native SRE tool below. No tool bypasses this, including the
  read-only ones.
- **Native, structured investigation tools** that call binaries directly via
  `exec.Command` with an argument list — no shell, no string interpolation,
  no injection surface — instead of asking the model to hand-build a
  `kubectl`/cloud-CLI invocation as free text every time:
  - `k8s_get`, `k8s_describe`, `k8s_logs` — read Kubernetes state, honoring
    `sre.kube_context` when set. `k8s_get` with `output: "json"` condenses
    pods/deployments/replicasets/statefulsets/daemonsets/nodes/services/
    events into a compact table (name, ready count, status, restarts, age,
    etc.) instead of the full manifest; pass `raw: true` to get the
    manifest back. Unrecognized kinds fall back to kubectl's own JSON
    unchanged.
  - `cloud_read` — read-only `aws`/`gcloud`/`az` calls. The tool only runs operations it recognizes as reads, and refuses everything else, including verbs it has never seen:
    - aws: `describe-*`, `list-*`, `get-*`, `filter-*`, and `s3 ls`;
    - gcloud: `list`, `describe`;
    - az: `list`, `show`.

    It also refuses reads that return secrets. All of this happens inside the tool, in addition to (not instead of) the governance check every call still goes through. Output is passed
    through as-is: aws/gcloud/az response shapes vary too much per
    subcommand to normalize generically and safely.
  - `metrics_query` — queries Prometheus, Grafana, or Datadog using the
    endpoints configured under `sre.observability`. Instant (`vector`/
    `scalar`) results condense to a METRIC/VALUE (or METRIC/SCOPE/LATEST for
    Datadog) table; range/matrix results are returned in full on purpose —
    condensing a time series would throw away the trend. `raw: true` always
    returns the full API response.
  - The general-purpose shell tool (`bash` or `powershell`) remains for anything these don't
    cover, and for applying real changes.
- Infra-aware classification of shell commands into DevMind's real
  `ChangeType` enum before they're evaluated, so the blast-radius invariant
  actually fires instead of silently no-oping on a raw command string:
  - **Terraform/OpenTofu** (`plan`/`apply`/`destroy`) → `terraform_apply`
  - **Kubernetes** (`apply`/`delete`/`scale`/`rollout`/`drain`/`cordon`/`patch`/`edit`/`replace`) → `k8s_manifest`
  - **Helm** (`install`/`upgrade`/`uninstall`/`rollback`) → `helm_release`
  - **IAM** across aws/gcloud/az → `iam_change`
  - **Secret rotation** (Secrets Manager, Secret Manager, Key Vault, Vault) → `secret_rotation`
  - **Schema migrations** (Alembic, Rails, Flyway, Prisma, Django) → `schema_migration`
  - **Direct destructive cloud calls** outside Terraform/Helm (e.g. `terminate-instances`, `delete-db-instance`) → `config_change`
  - Read-only cloud/k8s calls and observability queries are left unclassified
    on purpose, so they route through the low-risk generic policy engine
    instead of the infra engine.
- **Argv-based change classification.** Commands are split on shell
  separators, tokenized honoring quotes, stripped of wrappers (`VAR=x`,
  `sudo`, `env`, `npx`, `bundle exec`, ...), and classified by the real
  binary and subcommand, so `terraform -chdir=infra apply`, `kubectl
  --context prod delete`, `kubectl.exe delete`, `& terraform apply` and
  `bash -c "..."` are all caught. Opaque execution (`iex`,
  `Invoke-Expression`, `eval`, `pwsh -EncodedCommand`) goes to the infra
  engine. `terraform plan` is `terraform_plan`, not an apply. The evasion
  suite in `internal/tools/classify_test.go` only grows.
- **Environment resolution.** Declare your environments under
  `sre.environments` (kube contexts, cloud profiles, Terraform workspaces,
  repo paths → `prod|staging|dev`). Every call is resolved against them,
  including the `sre.kube_context` the native `k8s_*` tools inject; a
  whole-word keyword heuristic is only the fallback. An infrastructure
  change whose target cannot be resolved is evaluated as production.
- **Secrets are kept away from the model, best effort.**
  - `read_file` and `write_file` refuse credential files (`~/.aws/credentials`, kubeconfig, `.env*`, `*.pem`, `*.key`, `~/.ssh`, `*.tfstate`, ...). Paths are resolved first: `~`, relative paths and symlinks.
  - `cloud_read` refuses reads that return secret material, such as `secretsmanager get-secret-value` or `keyvault secret show`.
  - Every tool output is capped at `sre.max_tool_output_bytes` (default 30000; head and tail are kept) before it is sent to the provider.
  - Every tool output also goes through pattern-based redaction, which catches cloud keys, JWTs, tokens, URL credentials, `password=...` and Kubernetes Secret `data:`. Patterns cannot recognize every possible credential format, so do not rely on redaction alone. Run Grantline with credentials scoped to what the task needs.
- `grantline chat` keeps the conversation across turns; `/clear` resets it.
- Session IDs are `sess-<UTC timestamp>-<random>`, one `.jsonl` timeline per
  session (never appended to), and each record carries DevMind's
  `audit_id` so the local timeline joins DevMind's audit trail.
- Fail-closed governance: network errors, invalid HTTP responses, malformed
  responses, and unknown policy decisions become `REVIEW`, never `ALLOW`.
- Readable action traces containing the tool, decision, risk score, outcome,
  and session ID—never the action payload.
- **A persisted incident timeline.** Every `run` and `chat` session
  best-effort-appends its audit trail to `.grantline/sessions/<session-id>.jsonl`
  (same no-payload redaction as the live trace), so a postmortem can
  reconstruct what Grantline actually did after the terminal is long closed.
- **Runbooks.** `grantline run --runbook <name> "<extra context>"` loads
  `runbooks/<name>.json` and seeds the model's starting prompt and task hint
  from it — a standard starting procedure the model still adapts to what it
  actually finds, not a blind script. `grantline runbooks` lists what's
  available. Two examples ship in `runbooks/`: `pod-crashloop` and
  `high-latency`.
- **`--dry-run` rehearsal mode**, on both `grantline run` and `grantline chat`. A
  mutating tool call (the shell tool, `write_file`, or any MCP tool without an
  explicit `readOnlyHint` annotation) is skipped *before governance is even
  consulted* and reported back as `[dry-run] <tool> was not run`. Read-only
  tools (`k8s_get`, `cloud_read`, `metrics_query`, `read_file`) still run for
  real, so the model can actually investigate while rehearsing a runbook or
  a risky change. Any new mutating tool must declare `Tool.IsMutating`, or it
  is invisible to `--dry-run` — this exact gap (the flag was threaded through
  but silently never checked) was found and fixed during this audit; see the
  regression tests in `internal/tools/tools_test.go` prefixed `TestDryRun`.
- **MCP servers as first-class tools.** Configure a local stdio MCP server
  under `mcp_servers` in `grantline.config.json` and its tools appear in the same
  list as the native ones — same governance evaluation before every call,
  same audit trail, no special-casing anywhere else in the agent loop. An
  MCP tool without a `readOnlyHint`/`destructiveHint` annotation is treated
  as mutating by default for `--dry-run` purposes: an unlabelled remote
  action is never trusted. A per-call timeout (15s by default) means a
  hung server fails that one call instead of hanging the session; a
  misconfigured or unreachable server degrades that one integration
  (reported via a startup warning) instead of blocking Grantline from starting
  at all — the native tools always work regardless of MCP server health.
- **Session resume.** `grantline run --resume <session-id> "<new context>"` reads
  that session's persisted `.jsonl` timeline and folds a compact summary
  (tool, decision, outcome per call — never the raw payload) into the new
  session's starting prompt, so an on-call handoff doesn't lose state.
- **Per-session cost/latency accounting.** Every `run` prints and persists a
  `session_summary` record (input/output tokens, estimated cost from each
  provider's configured `cost_input_per_mtok`/`cost_output_per_mtok`, wall
  time) at the end of the run.
- `init`, `doctor` (checks the configured shell, `kubectl`,
  `terraform`/`tofu`, counts available runbooks, and proves the DevMind
  token works with one live read-only probe), `config` (prints the
  effective config with the token redacted), `run`, `runbooks`, and a test
  suite for the safety invariants, including regression tests that lock the
  `ChangeType` mapping to DevMind's actual enum (inventing a value like
  `terraform_destroy` gets rejected with HTTP 400 by the live API).

## Install

Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are attached to
every [release](https://github.com/mordecaiusm922-create/grantline/releases/latest).
Download the archive for your platform, extract `grantline`, and put it on your PATH.

Or install with Go:

```bash
go install github.com/mordecaiusm922-create/grantline/cmd/grantline@latest
grantline init
grantline doctor
grantline chat
```

`go install` puts the binary in `$(go env GOPATH)/bin` (on Windows,
`%USERPROFILE%\go\bin`); make sure that directory is on your PATH.

## Governance: local policy or DevMind

Every tool call is evaluated before it runs. Grantline picks the engine
automatically (`governance.engine: "auto"`, the default):

| | Local policy (no account) | DevMind |
|---|---|---|
| When | no `DEVMIND_TOKEN` set | `DEVMIND_TOKEN` set |
| Read-only tools (`k8s_get`, `k8s_logs`, `cloud_read`, ...) | run | run |
| Changes (shell, `write_file`, MCP tools) | always ask you first | deterministic verdict: safe changes run, risky ones ask, dangerous ones are blocked |
| Catastrophic commands (`rm -rf /`, `mkfs`, `curl ... \| sh`, `DROP DATABASE`, ...) | blocked | blocked |
| Org-wide destructive infra changes | escalated | escalated, with blast-radius and Terraform/Kubernetes semantics |
| Session risk memory, audit trail, team approvals | no | yes |

You can try Grantline right away with the local policy. To connect DevMind
(early access; [request a token](https://devmind-gamma.vercel.app/#request-access)):

```bash
export DEVMIND_TOKEN=dvm_...
grantline doctor
```

`grantline doctor` sends one harmless read-only probe and tells you exactly what
is wrong if it fails: missing token, rejected token (HTTP 401), a token
bound to a different agent (HTTP 403; set `GRANTLINE_AGENT_ID` to match), or
DevMind unreachable.

`DEVMIND_TOKEN` takes precedence over `governance.token` in
`grantline.config.json`. Prefer the environment variable so the token never sits
in a file; `grantline.config.json` is already in `.gitignore`. Set
`governance.engine` to `"devmind"` to require DevMind (Grantline refuses to start
without a token) or to `"local"` to always use the local policy. DevMind
failures fail closed: transient errors are retried, and anything that still
fails becomes `REVIEW`, never `ALLOW`.

## Build from source

Requires Go 1.24 or newer (older Go linkers omit the LC_UUID load command, so the binary will not launch on current macOS), plus the shell selected in `sre.shell` on PATH:
`bash` (default on Linux/macOS) or PowerShell 7+ (`pwsh`, default on Windows). Native investigation tools additionally need `kubectl`
and/or `aws`/`gcloud`/`az` on PATH for the ones you actually use; `grantline
doctor` reports what it finds.

```bash
go build -o grantline ./cmd/grantline
go test ./...
```

## Quick start

```bash
./grantline init
export OPENAI_API_KEY=sk-...
export DEVMIND_TOKEN=dvm_...   # optional: without it, the local policy governs
./grantline doctor
./grantline run --task planning "plan the EKS cluster upgrade to 1.31"
./grantline run --task security_review "review this Terraform change before it is applied"
./grantline run --task incident_response "checkout is returning 5xx, investigate and propose an action"
./grantline runbooks
./grantline run --runbook pod-crashloop "namespace checkout, pod checkout-7d9f restarting every 2 minutes"
./grantline run --dry-run --task security_review "apply the terraform change in infra/prod"
./grantline run --resume sess-1234 "error rate is still climbing, keep investigating"
```

## Non-interactive CI

Use `--non-interactive` to run Grantline as a gated check in CI. In this mode
`BLOCK` fails as usual, and `REVIEW`/`ESCALATE` abort without reading stdin:
a human review that CI cannot confirm is never approved automatically. All of
these cases exit non-zero. The minimal example in
`.github/workflows/grantline-example.yml` reviews a pull request's Terraform diff;
set `OPENAI_API_KEY` and `DEVMIND_TOKEN` as repository secrets before enabling
it. Grantline reads `DEVMIND_TOKEN` straight from the environment, so the token is
never written to a file.

To use MCP servers, add them to `grantline.config.json`:

```json
"mcp_servers": [
  { "name": "k8s-mcp", "command": "npx", "args": ["-y", "@some/k8s-mcp-server"] }
]
```

The sample configuration includes OpenAI, Groq, and a local vLLM server. It
also retains the built-in Ollama provider for users that prefer `/api/chat`.

```bash
ollama serve
ollama pull qwen2.5-coder:7b
```

## Governance contract

Governance is always on. `BLOCK` stops the action; `REVIEW` and `ESCALATE`
require the operator to type `y` unless `grantline run --non-interactive` is set,
in which case they fail closed; `REWRITE` is not executed.

For an intentionally unsafe local experiment only, set
`governance.enabled=false` and `GRANTLINE_UNSAFE_DISABLE_GOVERNANCE=1`. Grantline emits
a warning on every such session. This opt-out exists for development, not
production.

## Configuration

`grantline.config.json` is project-local. Start with `grantline init` or copy
`grantline.config.example.json`.

```json
{
  "default_provider": "openai",
  "providers": {
    "anthropic": { "model": "claude-sonnet-4-6" },
    "ollama": { "model": "qwen2.5-coder:7b", "base_url": "http://localhost:11434" }
  },
  "custom_providers": [
    {
      "id": "openai",
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "supports_tooling": true,
      "cost_input_per_mtok": 0.4,
      "cost_output_per_mtok": 1.6
    }
  ],
  "routing": [
    { "task": "planning", "provider": "openai" },
    { "task": "security_review", "provider": "openai" },
    { "task": "incident_response", "provider": "openai" }
  ],
  "governance": {
    "enabled": true,
    "engine": "auto",
    "base_url": "https://devmind-mcp.onrender.com/v1"
  },
  "sre": {
    "shell": "bash",
    "kube_context": "prod-eks",
    "cloud": { "provider": "aws", "profile": "prod" },
    "observability": {
      "prometheus_url": "http://localhost:9090",
      "datadog_site": "datadoghq.com",
      "datadog_api_key_env": "DATADOG_API_KEY",
      "datadog_app_key_env": "DATADOG_APP_KEY"
    },
    "runbooks_dir": "runbooks",
    "max_tool_output_bytes": 30000,
    "environments": [
      { "name": "prod-us", "tier": "prod", "kube_contexts": ["prod-eks"], "cloud_profiles": ["prod"], "paths": ["infra/live"] },
      { "name": "staging", "tier": "staging", "kube_contexts": ["stg-eks"], "paths": ["infra/staging"] }
    ]
  }
}
```

Every field under `sre` is optional and independently defaultable:
`kube_context` empty means "whatever kubectl's current-context already is";
`cloud.profile` empty means the CLI's own default credentials; every
`observability` endpoint is only required if you actually call
`metrics_query` against it; `runbooks_dir` defaults to `./runbooks`.

Every entry in `custom_providers` uses the OpenAI Chat Completions protocol.
To use Groq, Together, Fireworks, Mistral, DeepSeek, OpenRouter, vLLM,
llama.cpp server, or LM Studio, set its `id`, `base_url`, `model`, and (when
required) `api_key_env`; route a task to that ID. There is no legacy
`providers.openai` field.
Google Gemini works the same way with `base_url`
`https://generativelanguage.googleapis.com/v1beta/openai`.

A reasonable split for an SRE team: route `trivial_edit` to a fast/cheap
model, `planning` and `incident_response` to a fast tool-capable model, and
`security_review` (which now covers Terraform/K8s/IAM/secret-rotation/
destructive-cloud commands) to your strongest available model.

## Task classification

`grantline run` without `--task` classifies the prompt by keyword before routing:

1. **`incident_response`** — outage/down/degraded/SEV1/SEV2/on-call/PagerDuty language.
2. **`security_review`** — production, IAM, terraform/tofu, kubectl/helm, delete/drop/terminate/drain, secrets, migrations.
3. **`planning`** — plan/design/review/runbook/postmortem/capacity/SLO/SLA language.
4. **`trivial_edit`** — everything else.

Keyword classification is a heuristic starting point, not a substitute for
`--task` in CI, where the route must be reproducible.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for what is shipped and what comes next:
plan → policy → apply for Terraform/OpenTofu, Kubernetes diffs, approvals
outside the terminal (Slack) and a project context file.
