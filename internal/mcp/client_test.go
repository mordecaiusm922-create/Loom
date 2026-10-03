package mcp

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireSh skips the test on platforms without a POSIX shell (e.g. a bare
// Windows CI runner). The fake "servers" below are tiny sh scripts, not real
// MCP servers -- they exist to exercise StdioClient's framing/timeout/kill
// behavior without depending on a real MCP implementation being installed.
func requireSh(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh no disponible en este entorno; se omite el test de protocolo MCP")
	}
	return path
}

// TestDiscoverToolsRoundTrips starts a fake server that completes the MCP
// handshake before replying with a canned tools/list response.
func TestDiscoverToolsRoundTrips(t *testing.T) {
	sh := requireSh(t)
	script := `read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}\n' "$id"
read -r initialized
case "$initialized" in *'"method":"notifications/initialized"'*) ;; *) exit 1 ;; esac
read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo_tool","description":"repite el input","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}}\n' "$id"
`
	client, err := StartStdioServer(sh, "-c", script)
	if err != nil {
		t.Fatalf("StartStdioServer: %v", err)
	}
	defer client.Close()

	tools, err := client.DiscoverTools()
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo_tool" {
		t.Fatalf("tools = %+v, want one tool named echo_tool", tools)
	}
	if !tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("expected ReadOnlyHint to round-trip as true")
	}
}

func TestStartFailsWhenInitializeTimesOut(t *testing.T) {
	sh := requireSh(t)
	started := time.Now()
	client, err := StartStdioServerWithTimeout(100*time.Millisecond, sh, "-c", "read -r line; sleep 30")
	if err == nil {
		_ = client.Close()
		t.Fatal("StartStdioServerWithTimeout succeeded when initialize never answered")
	}
	if !strings.Contains(err.Error(), "inicializando servidor MCP") || !strings.Contains(err.Error(), "no respondio") {
		t.Fatalf("err = %v, want initialize timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("StartStdioServerWithTimeout took %s, want it to fail promptly", elapsed)
	}
}

// TestCallTimesOutOnHungServer is the regression test for the gap this
// hardening pass closes: a server that starts but never writes a response
// line must fail the call after the configured timeout instead of hanging
// the caller (and therefore the whole Loom session) forever.
func TestCallTimesOutOnHungServer(t *testing.T) {
	sh := requireSh(t)
	originalGrace := CloseGracePeriod
	CloseGracePeriod = 100 * time.Millisecond
	defer func() { CloseGracePeriod = originalGrace }()

	script := `read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}\n' "$id"
read -r initialized
sleep 30`
	// The handshake gets a generous timeout: spawning sh can take well over
	// 100ms on a loaded Windows/CI machine, and that is not what this test
	// is about. Only the call under test is held to the short timeout.
	client, err := StartStdioServerWithTimeout(10*time.Second, sh, "-c", script)
	if err != nil {
		t.Fatalf("StartStdioServerWithTimeout: %v", err)
	}
	defer client.Close()
	client.timeout = 100 * time.Millisecond

	start := time.Now()
	_, err = client.DiscoverTools()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "no respondio") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("call took %s, want it bounded near the 100ms timeout", elapsed)
	}
}

// TestCloseKillsHungProcess is the regression test for Close() itself
// hanging: a server that ignores stdin closing and never exits on its own
// must still let Close() return within roughly CloseGracePeriod, via
// SIGKILL, instead of blocking forever.
func TestCloseKillsHungProcess(t *testing.T) {
	sh := requireSh(t)
	originalGrace := CloseGracePeriod
	CloseGracePeriod = 150 * time.Millisecond
	defer func() { CloseGracePeriod = originalGrace }()

	// Ignore SIGTERM (Kill() below sends SIGKILL, which cannot be ignored,
	// but this also proves closing stdin alone isn't what ends the loop).
	script := `read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}\n' "$id"
read -r initialized
trap '' TERM; while true; do sleep 1; done`
	client, err := StartStdioServer(sh, "-c", script)
	if err != nil {
		t.Fatalf("StartStdioServer: %v", err)
	}

	start := time.Now()
	err = client.Close()
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "SIGKILL") {
		t.Fatalf("err = %v, want a SIGKILL-was-needed error", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close took %s, want it bounded near CloseGracePeriod", elapsed)
	}
}

// TestCloseReturnsPromptlyForACooperativeServer ensures the hardening above
// didn't regress the common case: a server that exits cleanly when stdin
// closes should not wait out the full grace period.
func TestCloseReturnsPromptlyForACooperativeServer(t *testing.T) {
	sh := requireSh(t)
	script := `read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1.0"}}}\n' "$id"
read -r initialized
cat >/dev/null`
	client, err := StartStdioServer(sh, "-c", script)
	if err != nil {
		t.Fatalf("StartStdioServer: %v", err)
	}

	start := time.Now()
	err = client.Close()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > CloseGracePeriod {
		t.Fatalf("Close took %s, want it to return promptly once the process exits on its own", elapsed)
	}
}
