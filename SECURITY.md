# Security Policy

Loom executes infrastructure commands on behalf of an AI model, so security
reports are the highest-priority issues in this project.

## Reporting a vulnerability

**Do not open a public issue for security problems.** Use GitHub's
[private vulnerability reporting](../../security/advisories/new) for this
repository instead.

Please include:

- the Loom version (`loom version`) and OS,
- the shell in use (`sre.shell`: `powershell` or `bash`),
- a minimal reproduction (prompt, config with secrets removed, expected vs.
  actual behavior),
- the impact you believe it has.

You should get an acknowledgement within 72 hours and a status update within
7 days. Please give us a reasonable window to ship a fix before disclosing.

## What is in scope

We especially want to hear about:

- **Governance bypass** — any way a tool call executes without a policy
  decision, or executes despite `BLOCK`, a rejected `REVIEW`/`ESCALATE`, or
  `REWRITE`.
- **Classification evasion** — an infrastructure change (Terraform, kubectl,
  Helm, cloud CLI) that reaches governance as a generic action instead of an
  infrastructure change, or with a lower blast radius than it really has.
- **Environment mis-resolution** — a production target resolved as
  staging/dev, which lowers the scrutiny it gets.
- **Secret exposure** — credentials reaching the model provider, the session
  timeline (`.loom/`), or logs despite redaction; reads of credential files
  that should be refused.
- **Injection** — prompt injection from tool output or MCP servers that leads
  to an action the operator did not ask for, or argument injection in the
  native tools (`k8s_*`, `cloud_read`, `metrics_query`).
- **`--dry-run` executing a mutating action.**

## Out of scope

- Running Loom with `LOOM_UNSAFE_DISABLE_GOVERNANCE=1` — that opt-out removes
  the guarantees on purpose.
- Vulnerabilities in the model providers, in DevMind's hosted service, or in
  third-party MCP servers (report those to their owners; DevMind issues can
  still be sent here and we will route them).
- Actions an operator explicitly approved at a `REVIEW`/`ESCALATE` prompt.

## Supported versions

Only the latest release receives security fixes while Loom is pre-1.0.
