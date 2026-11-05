# Grantline

[![ci](https://github.com/mordecaiusm922-create/grantline/actions/workflows/ci.yml/badge.svg)](https://github.com/mordecaiusm922-create/grantline/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**An AI agent harness for SRE, DevOps, Platform Engineering and Cloud Security
teams. Agents investigate your infrastructure freely and change it only with
your approval.**

Grantline runs an AI model inside a harness built for infrastructure work:
native tools for Kubernetes, cloud CLIs and metrics, awareness of which
environment is production, a deterministic policy gate in front of every
action, and a local audit trail. The model is replaceable (Anthropic, OpenAI,
Gemini, OpenRouter, Ollama, any OpenAI-compatible API). The gate is not.

It is not a general-purpose coding agent. Asked for application features, it
says so and stays in its domain: Terraform/OpenTofu, Kubernetes/Helm,
aws/gcloud/az, CI/CD, observability, incidents, IAM and secrets.

> Formerly **Loom** (until v0.5.x). `loom.config.json` and `.loom/` sessions
> are still read; `grantline init` migrates the config.

## What it looks like

An abridged `grantline chat` session with the built-in local policy:

```text
› why is checkout restarting in payments?
  │ ALLOW   k8s_get    risk 0.0 · completed
  │ ALLOW   k8s_logs   risk 0.0 · completed
● checkout-7d9f restarted 14 times in 30 min, all OOMKilled (heap 512Mi = limit 512Mi).

› raise its memory limit to 1Gi
    Policy: REVIEW   risk 60.0
    • infrastructure change (k8s_manifest) needs your confirmation
    continue anyway?  [y] yes  [n] no

› run exactly: psql -c "DROP DATABASE payments"
  │ BLOCK   bash       risk 100.0 · blocked
● The local policy blocked it (database drop is always blocked). It was not executed.
```

Every action gets a verdict before it runs:

| Verdict | With the built-in local policy |
|---|---|
| **ALLOW** | Reads: the native read-only tools, and shell lines made only of recognized reads (`ls`, `cat`, `kubectl get`, `git status`, `aws ... describe-*`) |
| **REVIEW** | Anything that can change state: you approve or reject it |
| **ESCALATE** | Destructive infrastructure changes on production with organization-wide blast radius |
| **BLOCK** | Catastrophic commands (`rm -rf /`, `mkfs`, `curl ... \| sh`, `DROP DATABASE`, ...) never run |

## Install

**Prebuilt binaries** for Linux, macOS and Windows (amd64, arm64) are on the
[releases page](https://github.com/mordecaiusm922-create/grantline/releases/latest).

**With Go** (1.24 or newer):

```bash
go install github.com/mordecaiusm922-create/grantline/cmd/grantline@latest
```

The binary lands in `$(go env GOPATH)/bin`; make sure it is on your PATH.

**From source:**

```bash
git clone https://github.com/mordecaiusm922-create/grantline
cd grantline
go build -o grantline ./cmd/grantline
```

## First run (two minutes)

**Linux / macOS**

```bash
grantline init                          # writes grantline.config.json (owner-only permissions)
export ANTHROPIC_API_KEY=sk-ant-...     # the default config uses Anthropic
grantline doctor                        # checks keys, shell, kubectl/terraform and the policy engine
grantline chat
```

**Windows (PowerShell).** Run one command per line; Windows PowerShell 5.1
does not accept `&&`.

```powershell
$env:Path += ";$(go env GOPATH)\bin"
grantline init
$env:ANTHROPIC_API_KEY = "sk-ant-..."   # quotes required; paste the complete key
grantline doctor
grantline chat
```

The agent runs commands with PowerShell 7 (`pwsh`); install it with
`winget install Microsoft.PowerShell` if `doctor` asks for it.

`grantline doctor` never prints a key. It reports a key that is missing, that
contains pasted control characters, or that does not start with its
provider's usual prefix (`sk-ant-`, `sk-or-`, `AIza`, ...).

No account is needed: the built-in local policy governs every action.

### Using another model provider

Any OpenAI-compatible API works. Add it to `custom_providers` in
`grantline.config.json` and route the tasks to it:

```json
{
  "default_provider": "openrouter",
  "custom_providers": [
    {
      "id": "openrouter",
      "base_url": "https://openrouter.ai/api/v1",
      "model": "<model id>",
      "api_key_env": "OPENROUTER_API_KEY",
      "supports_tooling": true
    }
  ],
  "routing": [
    { "task": "trivial_edit", "provider": "openrouter" },
    { "task": "planning", "provider": "openrouter" },
    { "task": "security_review", "provider": "openrouter" },
    { "task": "incident_response", "provider": "openrouter" }
  ]
}
```

| Provider | `base_url` |
|---|---|
| OpenAI | `https://api.openai.com/v1` |
| Google Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` |
| OpenRouter | `https://openrouter.ai/api/v1` |
| Groq | `https://api.groq.com/openai/v1` |
| vLLM, llama.cpp, LM Studio | your local server, e.g. `http://localhost:8000/v1` |

Ollama is also built in (`providers.ollama`). Models with weak tool calling
make more mistakes; Grantline still governs every call they make.

## How SRE teams use it

**Investigating an incident.** Point it at the symptom. It reads pods, logs,
cloud resources and metrics without interrupting you, keeps a step plan, and
asks before any mitigation.

```bash
grantline run --runbook pod-crashloop "namespace checkout, pod restarting every 2 minutes"
grantline run --resume sess-20261105T091512Z-7f3a9c01 "error rate still climbing"
```

**Reviewing a change.** Ask it to check a Terraform or Kubernetes change
before it is applied. `terraform plan` is allowed; `apply`, `destroy`,
`kubectl apply/delete/scale` and `helm upgrade` always need you.

```bash
grantline run --task security_review "review the terraform change in infra/prod"
grantline run --dry-run --task security_review "apply the terraform change in infra/prod"
```

`--dry-run` lets reads run for real and skips every action that could change
state, so you can rehearse a runbook safely.

**A gate in CI.** With `--non-interactive`, BLOCK fails, and REVIEW and
ESCALATE fail too instead of waiting for input: CI never approves on a
person's behalf. Every failure exits non-zero. See
[`.github/workflows/grantline-example.yml`](.github/workflows/grantline-example.yml).

## How the safety works

No single layer is trusted on its own:

| Layer | What it does | Limit |
|---|---|---|
| Policy gate | Every tool call, reads included, is evaluated before it runs. Engine errors and unknown verdicts become REVIEW, never ALLOW | The local policy is conservative and general; DevMind adds team rules |
| Argv-based classification | Shell lines are split and tokenized as a shell would, wrappers (`sudo`, `env`, `npx`) are stripped, and the real binary and subcommand decide. `terraform -chdir=x apply`, `kubectl --context prod delete`, `bash -c "..."` and `iex` are all caught | New evasion techniques are added to the test suite as they are found |
| Read allowlist | A shell line runs without asking only if every command on it is a recognized read. Redirection, `$` expansion, wrappers, globs, recursive grep, PowerShell `env:` drives and reads of protected files stay in REVIEW. A labelled corpus fails CI on any false ALLOW | Unrecognized reads ask; that is the intended trade-off |
| Environment awareness | Calls are resolved against the environments you declare (kube contexts, cloud profiles, paths). An unresolved infrastructure target is treated as production | Undeclared environments fall back to name heuristics |
| Protected paths | File tools and shell reads refuse credentials (`~/.aws/credentials`, kubeconfig, `.env*`, keys, `*.tfstate`) and the session audit trail | Covers common credential locations, not every possible one |
| Output redaction | Tool output is capped and redacted (cloud keys, JWTs, tokens, URL credentials, Kubernetes Secret data) before it reaches the model | Pattern-based, best effort: run Grantline with credentials scoped to the task |
| Native read-only tools | `k8s_*`, `cloud_read` and `metrics_query` call binaries with an argument list, never a shell. `cloud_read` runs only recognized reads and refuses reads that return secrets | — |
| Audit trail | One `.jsonl` timeline per session in `.grantline/sessions/`: tool, verdict, risk and outcome, never the payload | Local to the machine that ran it |

Disabling governance for a local experiment requires both
`governance.enabled=false` and `GRANTLINE_UNSAFE_DISABLE_GOVERNANCE=1`, and
every such session prints a warning.

## DevMind: rules and approvals for teams

The local policy protects one person at a terminal. DevMind is the optional
policy engine for teams: deterministic verdicts with Terraform and Kubernetes
semantics, risk memory per person and per agent, and an audit trail your
security team can read. No LLM sits in the decision path.

```bash
export DEVMIND_TOKEN=dvm_...    # early access: https://devmind-gamma.vercel.app/#request-access
grantline doctor                # proves the token with one read-only probe
```

With `governance.engine: "auto"` (the default), Grantline uses DevMind when a
token is set and the local policy otherwise. Set `"devmind"` to require it,
or `"local"` to never use it. DevMind failures are retried, then fail closed
to REVIEW.

## Reference

### Commands

| Command | Purpose |
|---|---|
| `grantline chat [--dry-run]` | Interactive terminal UI |
| `grantline run [flags] "<prompt>"` | One governed task |
| `grantline runbooks` | List the runbooks in `runbooks/` |
| `grantline init` | Create `grantline.config.json` (never overwrites) |
| `grantline doctor` | Check keys, shell, tools and the policy engine |
| `grantline config` | Print the effective config, token redacted |
| `grantline version` | Print the version |

`run` flags: `--task <trivial_edit|planning|security_review|incident_response>`,
`--runbook <name>`, `--resume <session-id>`, `--dry-run`, `--non-interactive`.
Without `--task` the prompt is classified by keyword; pass `--task` in CI so
the route is reproducible.

### Native tools

| Tool | Does |
|---|---|
| `k8s_get`, `k8s_describe`, `k8s_logs` | Read Kubernetes state, honoring `sre.kube_context`; common kinds are condensed to a table (`raw: true` for the full manifest) |
| `cloud_read` | aws `describe-/list-/get-/lookup-/search-/batch-get-/filter-` and `s3 ls`; gcloud `list/describe/get-iam-policy/read`; az `list/show` |
| `metrics_query` | Prometheus, Grafana or Datadog, from `sre.observability` |
| `read_file`, `write_file` | Files, with protected paths refused |
| `update_plan` | The step plan shown in `chat` |
| `bash` or `powershell` | The configured shell (`sre.shell`), for everything else |
| MCP servers | Tools from local stdio MCP servers listed in `mcp_servers`, under the same governance |

### Configuration

`grantline.config.json` lives in your project. Every `sre` field is optional:

```json
{
  "governance": { "enabled": true, "engine": "auto" },
  "sre": {
    "shell": "bash",
    "kube_context": "prod-eks",
    "cloud": { "provider": "aws", "profile": "prod" },
    "observability": { "prometheus_url": "http://localhost:9090" },
    "runbooks_dir": "runbooks",
    "max_tool_output_bytes": 30000,
    "environments": [
      { "name": "prod-us", "tier": "prod", "kube_contexts": ["prod-eks"], "cloud_profiles": ["prod"], "paths": ["infra/live"] },
      { "name": "staging", "tier": "staging", "kube_contexts": ["stg-eks"], "paths": ["infra/staging"] }
    ]
  },
  "mcp_servers": [
    { "name": "k8s-mcp", "command": "npx", "args": ["-y", "@some/k8s-mcp-server"] }
  ]
}
```

`grantline.config.example.json` has a fuller example with several providers
and per-task routing.

### Environment variables

| Variable | Purpose |
|---|---|
| `ANTHROPIC_API_KEY`, or each custom provider's `api_key_env` | Model provider keys |
| `DEVMIND_TOKEN` | Connects DevMind; takes precedence over `governance.token` |
| `GRANTLINE_AGENT_ID` | The agent identity sent to DevMind (default `grantline-sre`) |
| `GRANTLINE_UNSAFE_DISABLE_GOVERNANCE` | Local development only; see above |

## Where it is going

Teams will define their own agents in a file and deploy them where they work:
terminal, GitHub Actions, Kubernetes. This format is **planned, not available
yet**:

```yaml
# agents/tf-change.yaml
name: tf-change
role: devops                 # sre | devops | platform | cloudsec
workflow: change             # plan -> review -> apply
trigger: pull_request
environments: [staging]      # prod only when listed explicitly
tools: [cloud_read, k8s_get, terraform]
policy:
  review_in: [prod]          # can add restrictions, never relax them
output: [plan_file, pr_comment]
```

After that, the reviewed plan becomes the credential: an apply runs with
short-lived cloud credentials limited to that plan, enforced by the cloud
provider rather than by the agent. Order and details are in
[ROADMAP.md](ROADMAP.md).

## Feedback and security

Tried it? [Open an issue](https://github.com/mordecaiusm922-create/grantline/issues/new);
even "this confused me" helps. Report vulnerabilities privately, as described
in [SECURITY.md](SECURITY.md).

## Build and test

```bash
go build -o grantline ./cmd/grantline
go test ./...
```

Licensed under Apache-2.0. See [LICENSE](LICENSE).
