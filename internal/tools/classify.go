package tools

import (
	"strings"
	"unicode"
)

// DevMind ChangeType enum values (core/types.py). Inventing a value such as
// "terraform_destroy" is rejected with HTTP 400 by the live API; destructive
// intent travels in the payload and blast_radius, not in change_type.
const (
	changeTerraformPlan  = "terraform_plan"
	changeTerraformApply = "terraform_apply"
	changeK8sManifest    = "k8s_manifest"
	changeHelmRelease    = "helm_release"
	changeIAM            = "iam_change"
	changeSecretRotation = "secret_rotation"
	changeConfig         = "config_change"
	changeSchemaMigrate  = "schema_migration"
)

// changePriority orders classifications when one command line contains
// several commands: the riskiest one decides where the whole line is
// evaluated.
var changePriority = map[string]int{
	changeTerraformPlan:  1,
	changeSchemaMigrate:  2,
	changeHelmRelease:    3,
	changeK8sManifest:    4,
	changeSecretRotation: 5,
	changeConfig:         6,
	changeTerraformApply: 7,
	changeIAM:            8,
}

// iacChangeType classifies a shell command line as an infrastructure change.
//
// It works on argv, not on substrings of the raw text: the line is split
// into commands on shell separators (; & | newline, command substitution),
// each command is tokenized honoring quotes, wrappers such as env
// assignments/sudo/time are skipped, argv[0] is normalized (basename,
// lowercase, no .exe), and global flags are skipped before looking at the
// subcommand. The substring version missed `terraform -chdir=infra apply`,
// `kubectl  delete` (two spaces), `kubectl.exe delete`, `kubectl --context
// prod delete` and `& terraform apply`.
//
// Opaque execution (`iex`, `Invoke-Expression`, `eval`, `pwsh
// -EncodedCommand`) cannot be inspected and is classified as config_change,
// so it reaches the infra engine instead of the generic policy engine.
func iacChangeType(command string) (changeType string, isChange bool) {
	return classifyLine(command, 0)
}

const maxNesting = 3

func classifyLine(line string, depth int) (string, bool) {
	best := ""
	for _, argv := range splitCommands(line) {
		changeType, ok := classifyArgv(argv, depth)
		if ok && changePriority[changeType] > changePriority[best] {
			best = changeType
		}
	}
	return best, best != ""
}

func classifyArgv(argv []string, depth int) (string, bool) {
	argv = stripWrappers(argv)
	if len(argv) == 0 {
		return "", false
	}
	bin := normalizeBinary(argv[0])
	args := argv[1:]
	lowerArgs := make([]string, len(args))
	for i, a := range args {
		lowerArgs[i] = strings.ToLower(a)
	}

	switch bin {
	case "sh", "bash", "zsh", "dash", "ksh", "pwsh", "powershell", "cmd":
		for i, a := range lowerArgs {
			switch a {
			case "-encodedcommand", "-enc", "-ec", "-e":
				if bin == "pwsh" || bin == "powershell" {
					return changeConfig, true
				}
			case "-c", "-command", "/c":
				if i+1 < len(args) {
					if depth >= maxNesting {
						return changeConfig, true
					}
					return classifyLine(strings.Join(args[i+1:], " "), depth+1)
				}
			}
		}
		return "", false
	case "iex", "invoke-expression", "eval":
		return changeConfig, true
	case "ssh":
		// ssh [flags] host command... runs the remote command verbatim.
		host := firstPositional(args, sshValueFlags)
		if i := indexOf(args, host); host != "" && i+1 < len(args) && depth < maxNesting {
			return classifyLine(strings.Join(args[i+1:], " "), depth+1)
		}
		return "", false
	case "terraform", "tofu", "tf", "terragrunt":
		sub := firstPositional(lowerArgs, nil)
		if bin == "terragrunt" && (sub == "run-all" || sub == "run") {
			sub = firstPositional(lowerArgs[indexOf(lowerArgs, sub)+1:], nil)
		}
		switch sub {
		case "plan":
			return changeTerraformPlan, true
		case "state":
			// state list/show/pull are reads; rm/mv/push/replace-provider rewrite state.
			switch firstPositional(lowerArgs[indexOf(lowerArgs, sub)+1:], nil) {
			case "list", "show", "pull", "":
				return "", false
			}
			return changeTerraformApply, true
		case "apply", "destroy", "import", "taint", "untaint", "force-unlock", "refresh":
			return changeTerraformApply, true
		}
		return "", false
	case "kubectl", "k", "oc", "kubecolor":
		sub := firstPositional(lowerArgs, kubectlValueFlags)
		switch sub {
		case "rollout":
			rest := lowerArgs[indexOf(lowerArgs, sub)+1:]
			if action := firstPositional(rest, kubectlValueFlags); action == "status" || action == "history" {
				return "", false
			}
			return changeK8sManifest, true
		case "apply", "delete", "scale", "drain", "cordon", "uncordon", "patch", "edit",
			"replace", "create", "set", "label", "annotate", "taint", "autoscale", "expose",
			"run", "exec", "cp", "debug":
			return changeK8sManifest, true
		}
		return "", false
	case "helm":
		switch firstPositional(lowerArgs, helmValueFlags) {
		case "install", "upgrade", "uninstall", "delete", "del", "rollback":
			return changeHelmRelease, true
		}
		return "", false
	case "aws", "gcloud", "az":
		return classifyCloud(bin, lowerArgs)
	case "vault":
		sub := firstPositional(lowerArgs, nil)
		if sub == "write" || sub == "delete" || sub == "patch" {
			return changeSecretRotation, true
		}
		if sub == "kv" {
			switch firstPositional(lowerArgs[indexOf(lowerArgs, sub)+1:], nil) {
			case "put", "patch", "delete", "destroy", "rollback", "undelete", "metadata":
				return changeSecretRotation, true
			}
		}
		return "", false
	}
	if isMigration(bin, lowerArgs) {
		return changeSchemaMigrate, true
	}
	return "", false
}

func classifyCloud(bin string, args []string) (string, bool) {
	service := firstPositional(args, cloudValueFlags)
	mutating := isCloudMutatingCommand(args)
	switch {
	case bin == "aws" && service == "iam" && mutating,
		bin == "gcloud" && service == "iam" && mutating,
		bin == "gcloud" && containsAny(args, "add-iam-policy-binding", "remove-iam-policy-binding", "set-iam-policy"),
		bin == "az" && (service == "role" || service == "ad") && mutating:
		return changeIAM, true
	case bin == "aws" && service == "secretsmanager" && containsAny(args, "rotate-secret", "put-secret-value", "update-secret", "delete-secret"),
		bin == "aws" && service == "ssm" && containsAny(args, "put-parameter", "delete-parameter"),
		bin == "gcloud" && service == "secrets" && mutating,
		bin == "az" && service == "keyvault" && containsAny(args, "secret") && mutating:
		return changeSecretRotation, true
	case isDestructiveArgs(args):
		return changeConfig, true
	}
	return "", false
}

func isMigration(bin string, args []string) bool {
	switch bin {
	case "alembic":
		return containsAny(args, "upgrade", "downgrade", "stamp")
	case "flyway", "liquibase":
		return containsAny(args, "migrate", "update", "clean", "rollback", "baseline", "repair")
	case "prisma":
		return containsAny(args, "migrate") && containsAny(args, "deploy", "reset", "dev")
	case "atlas", "migrate", "goose", "dbmate":
		return containsAny(args, "apply", "up", "down", "reset", "migrate", "rollback")
	case "python", "python3", "py":
		return containsAny(args, "manage.py") && containsAny(args, "migrate")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "db:migrate") || strings.HasPrefix(a, "db:rollback") || a == "db:schema:load" || a == "db:drop" || a == "db:reset" {
			return true
		}
	}
	return false
}

var kubectlValueFlags = map[string]bool{
	"--context": true, "-n": true, "--namespace": true, "--kubeconfig": true, "--cluster": true,
	"--user": true, "-s": true, "--server": true, "--as": true, "--as-group": true, "--token": true,
	"--request-timeout": true, "-l": true, "--selector": true, "-f": true, "--filename": true, "-o": true, "--output": true,
}

var helmValueFlags = map[string]bool{
	"-n": true, "--namespace": true, "--kube-context": true, "--kubeconfig": true, "--kube-token": true,
	"--kube-apiserver": true, "--registry-config": true, "--repository-config": true,
}

var sshValueFlags = map[string]bool{
	"-i": true, "-p": true, "-l": true, "-o": true, "-F": true, "-J": true, "-L": true, "-R": true, "-D": true,
}

var cloudValueFlags = map[string]bool{
	"--profile": true, "--region": true, "--output": true, "--endpoint-url": true, "--project": true,
	"--subscription": true, "--configuration": true, "--account": true, "--format": true,
}

// firstPositional returns the first argument that is not a flag, skipping
// the value of flags known to take a separate value ("--context prod").
func firstPositional(args []string, valueFlags map[string]bool) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

func indexOf(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return len(args) - 1
}

func containsAny(args []string, targets ...string) bool {
	for _, a := range args {
		for _, t := range targets {
			if a == t {
				return true
			}
		}
	}
	return false
}

var destructiveSegments = map[string]bool{
	"destroy": true, "delete": true, "uninstall": true, "drop": true, "terminate": true,
	"purge": true, "deallocate": true, "drain": true, "deprovision": true, "rm": true, "rb": true,
}

func isDestructiveArgs(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		for _, segment := range strings.Split(a, "-") {
			if destructiveSegments[segment] {
				return true
			}
		}
	}
	return false
}

// stripWrappers drops leading env assignments (FOO=bar), the PowerShell
// call operator's leftovers, and wrapper commands that run their argument
// as a command (sudo, time, env, nohup, ...), including their own flags.
func stripWrappers(argv []string) []string {
	for len(argv) > 0 {
		head := argv[0]
		lower := normalizeBinary(head)
		switch {
		case strings.Contains(head, "=") && !strings.HasPrefix(head, "-") && !strings.ContainsAny(head, `/\`):
			argv = argv[1:]
		case lower == "sudo" || lower == "doas" || lower == "time" || lower == "env" || lower == "nohup" ||
			lower == "command" || lower == "exec" || lower == "xargs" || lower == "watch" || lower == "timeout" ||
			lower == "nice" || lower == "stdbuf" || lower == "start-process" || lower == "npx" || lower == "bunx":
			argv = argv[1:]
			for len(argv) > 0 && (strings.HasPrefix(argv[0], "-") || (lower == "timeout" && isNumberish(argv[0]))) {
				argv = argv[1:]
			}
		case len(argv) > 1 && (lower == "bundle" || lower == "poetry" || lower == "uv" || lower == "pipenv" || lower == "pnpm" || lower == "yarn") &&
			(strings.ToLower(argv[1]) == "exec" || strings.ToLower(argv[1]) == "run"):
			argv = argv[2:]
		default:
			return argv
		}
	}
	return argv
}

func isNumberish(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' && r != 's' && r != 'm' }) == -1
}

func normalizeBinary(token string) string {
	token = strings.ToLower(strings.Trim(token, `"'`))
	if i := strings.LastIndexAny(token, `/\`); i >= 0 {
		token = token[i+1:]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		token = strings.TrimSuffix(token, ext)
	}
	return token
}

// splitCommands tokenizes a command line into one argv per command. Quotes
// group words and are removed; separators outside quotes end a command:
// ; & | newline ( ) { } and backtick, so `$(terraform apply)` and
// `& terraform apply` both surface terraform as argv[0].
func splitCommands(line string) [][]string {
	var commands [][]string
	var argv []string
	var current strings.Builder
	inToken := false
	var quote rune

	flushToken := func() {
		if inToken {
			argv = append(argv, current.String())
			current.Reset()
			inToken = false
		}
	}
	flushCommand := func() {
		flushToken()
		if len(argv) > 0 {
			commands = append(commands, argv)
			argv = nil
		}
	}

	for _, r := range line {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
			inToken = true
		case r == ';' || r == '&' || r == '|' || r == '\n' || r == '\r' || r == '(' || r == ')' || r == '{' || r == '}' || r == '`':
			flushCommand()
		case unicode.IsSpace(r):
			flushToken()
		default:
			current.WriteRune(r)
			inToken = true
		}
	}
	flushCommand()
	return commands
}
