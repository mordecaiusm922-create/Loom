package tools

import (
	"path/filepath"
	"regexp"
	"strings"
)

// isReadOnlyShellLine reports whether every command on a shell line is a
// recognized read that can neither change state nor pull a secret into the
// model conversation. It is an allowlist: anything it does not recognize
// returns false and keeps going through REVIEW, so a false "true" is the
// only failure that matters. The cases are pinned by shellReadCorpus in
// shellread_test.go.
//
// A line is rejected as a whole when:
//   - any token redirects (> or <) or expands a variable ($): redirection
//     writes files, and expansion can print secrets ($env:API_KEY);
//   - a command runs through a wrapper (sudo, env, xargs, timeout...);
//   - a command reads a file whose path is protected (credentials, .env,
//     Terraform state, the session audit trail) or uses a glob, which the
//     shell could expand to a protected path;
//   - a command touches a PowerShell provider drive (env:, variable:,
//     cert:, hklm:...), which exposes environment variables and secrets.
func isReadOnlyShellLine(line string) bool {
	commands := splitCommands(line)
	if len(commands) == 0 {
		return false
	}
	for _, argv := range commands {
		if !isReadOnlyArgv(argv) {
			return false
		}
	}
	return true
}

// psDrive matches a PowerShell provider path such as env:, Env:\PATH or
// HKLM:\Software. Single-letter file system drives (C:\) are not providers.
var psDrive = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]+:`)

func isReadOnlyArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if len(stripWrappers(argv)) != len(argv) {
		return false
	}
	for _, token := range argv {
		if strings.ContainsAny(token, "<>$") {
			return false
		}
		for _, v := range tokenValues(token) {
			if psDrive.MatchString(v) {
				return false
			}
		}
	}
	bin := normalizeBinary(argv[0])
	args := argv[1:]
	lower := make([]string, len(args))
	for i, a := range args {
		lower[i] = strings.ToLower(a)
	}

	switch bin {
	// Inspect the session or the host; no arguments that change anything.
	case "pwd", "whoami", "id", "uname", "uptime", "free", "df", "du", "ps", "which", "where",
		"get-location", "gl", "get-date", "get-process", "gps", "get-service", "gsv",
		"get-command", "gcm", "test-path", "resolve-path", "measure-object", "measure", "write-output":
		return true
	case "echo":
		return true // $ was rejected above, so nothing is expanded
	case "hostname":
		return len(args) == 0 // `hostname NAME` and `hostname -F file` set it
	case "date":
		return !containsAny(lower, "-s", "--set")
	case "tree":
		return !containsAny(lower, "-o")
	case "stat", "file", "wc":
		return pathsAreReadable(args)

	// List directories: names only, never file contents.
	case "ls", "dir", "gci", "get-childitem", "get-item", "gi":
		return !hasGlob(args)
	case "find":
		for _, a := range lower {
			switch a {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				return false
			}
		}
		return true

	// Read file contents: every path must be readable through read_file too.
	case "cat", "head", "tail", "type", "gc", "get-content", "more":
		return pathsAreReadable(args)
	case "grep", "select-string", "sls":
		// A recursive search reads every file under a directory, protected
		// ones included. Short flags can be clustered (-ri, -Rn).
		for _, a := range args {
			if a == "--recursive" || a == "--dereference-recursive" ||
				(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")) {
				return false
			}
		}
		return pathsAreReadable(args)

	case "git":
		return isReadOnlyGit(lower)
	case "kubectl", "k", "oc", "kubecolor":
		return isReadOnlyKubectl(lower)
	case "helm":
		switch firstPositional(lower, helmValueFlags) {
		case "list", "ls", "status", "history", "version":
			return true
		}
		return false
	case "terraform", "tofu":
		switch firstPositional(lower, nil) {
		case "version", "validate", "providers":
			return true
		case "state":
			return firstPositional(lower[indexOf(lower, "state")+1:], nil) == "list"
		}
		return false
	case "docker", "podman":
		switch firstPositional(lower, nil) {
		case "ps", "images", "version":
			return true
		}
		return false
	case "aws", "gcloud", "az":
		return isCloudReadOnlyCommand(bin, lower)
	}
	return false
}

func isReadOnlyGit(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--exec") || strings.HasPrefix(a, "-c") {
			return false
		}
	}
	switch firstPositional(args, nil) {
	case "status", "log", "diff", "show", "rev-parse", "ls-files", "blame", "shortlog":
		return true
	case "branch":
		// Listing only: -d/-D/-m/-c and a new branch name all change refs.
		for _, a := range args[indexOf(args, "branch")+1:] {
			switch a {
			case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--list", "--show-current":
			default:
				return false
			}
		}
		return true
	case "remote":
		rest := args[indexOf(args, "remote")+1:]
		return len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose"))
	}
	return false
}

func isReadOnlyKubectl(args []string) bool {
	// Secret data must not reach the model through a shell read; the native
	// tools handle Secrets on purpose.
	for _, a := range args {
		if a == "--raw" || strings.HasPrefix(a, "--raw=") {
			return false // the raw API path can read anything, Secrets included
		}
		if a == "secret" || a == "secrets" || strings.HasPrefix(a, "secret/") || strings.HasPrefix(a, "secrets/") {
			return false
		}
	}
	switch firstPositional(args, kubectlValueFlags) {
	case "get", "describe", "logs", "top", "version", "api-resources", "api-versions", "explain":
		return true
	}
	return false
}

// pathsAreReadable applies read_file's protection to every non-flag
// argument: a protected path, or a glob that could expand to one, keeps
// the command in REVIEW.
func pathsAreReadable(args []string) bool {
	if hasGlob(args) {
		return false
	}
	for _, a := range args {
		for _, v := range tokenValues(a) {
			if strings.HasPrefix(v, "-") || v == "" {
				continue
			}
			if sensitivePathReason(v) != "" || isSecretBearingPath(v) {
				return false
			}
		}
	}
	return true
}

// tokenValues returns the token and, for a flag written with its value in
// the same token (--file=.env, PowerShell's -Path:.env), that value too, so
// the value gets the same checks as a separate argument.
func tokenValues(token string) []string {
	if !strings.HasPrefix(token, "-") {
		return []string{token}
	}
	if i := strings.IndexAny(token, "=:"); i > 0 && i+1 < len(token) {
		return []string{token, token[i+1:]}
	}
	return []string{token}
}

func hasGlob(args []string) bool {
	for _, a := range args {
		if strings.ContainsAny(a, "*?[") {
			return true
		}
	}
	return false
}

// isSecretBearingPath covers files that are not credentials themselves but
// routinely contain them: a process environment (/proc/<pid>/environ holds
// every exported API key) and shell histories (a key typed on the command
// line stays there).
func isSecretBearingPath(path string) bool {
	resolved := strings.ToLower(filepath.ToSlash(resolvePath(path)))
	for _, p := range []string{strings.ToLower(filepath.ToSlash(path)), resolved} {
		if p == "/proc" || strings.HasPrefix(p, "/proc/") {
			return true
		}
	}
	base := filepath.Base(resolved)
	return strings.HasSuffix(base, "_history") || base == "consolehost_history.txt"
}
