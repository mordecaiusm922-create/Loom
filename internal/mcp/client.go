// Package mcp implementa un cliente mínimo del Model Context Protocol
// sobre stdio. Esto es deliberadamente un subconjunto del spec --
// suficiente para listar y llamar herramientas de un servidor MCP
// local (ej. `python devmind_server.py`) -- no una implementación
// completa de streamable-http/SSE, que es el siguiente paso natural
// para conectar contra devmind-mcp.onrender.com directamente.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// DefaultTimeout bounds how long a single JSON-RPC call waits for a
// response line before giving up. A hung/misbehaving MCP server must not be
// able to hang an entire Loom session -- this matters most exactly when it
// would hurt most, mid-incident.
const DefaultTimeout = 15 * time.Second

const protocolVersion = "2024-11-05"

// CloseGracePeriod is how long Close waits for the subprocess to exit on
// its own (after stdin is closed) before sending SIGKILL. Tests lower this
// var to keep the suite fast; production code should leave it alone.
var CloseGracePeriod = 3 * time.Second

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// StdioClient lanza un servidor MCP como subproceso y habla JSON-RPC
// línea por línea sobre su stdin/stdout.
type StdioClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	nextID  int
	timeout time.Duration
}

// ToolDefinition is the portion of an MCP tools/list response Loom needs to
// expose a remote tool through its normal governed registry.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    bool `json:"readOnlyHint"`
		DestructiveHint bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// StartStdioServer starts a server with DefaultTimeout for every call and
// completes the MCP initialization handshake before returning it to callers.
func StartStdioServer(command string, args ...string) (*StdioClient, error) {
	return StartStdioServerWithTimeout(DefaultTimeout, command, args...)
}

// StartStdioServerWithTimeout is StartStdioServer with an explicit per-call
// timeout, mainly for tests that need it much shorter than DefaultTimeout.
// The timeout also bounds the mandatory initialization handshake, so callers
// never receive a client that is only partially initialized.
func StartStdioServerWithTimeout(timeout time.Duration, command string, args ...string) (*StdioClient, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := &StdioClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		nextID:  1,
		timeout: timeout,
	}
	if err := client.initialize(); err != nil {
		client.abort()
		return nil, fmt.Errorf("inicializando servidor MCP: %w", err)
	}
	return client, nil
}

// initialize performs the MCP lifecycle required before tools/list or any
// other request. It lives at the constructor boundary rather than burdening
// each caller, preserving the existing ready-to-use StdioClient API.
func (c *StdioClient) initialize() error {
	if _, err := c.call("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "loom",
			"version": "0.4.0-sre",
		},
	}); err != nil {
		return err
	}
	notification, err := json.Marshal(rpcNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(notification, '\n')); err != nil {
		return fmt.Errorf("enviando notifications/initialized: %w", err)
	}
	return nil
}

// call is bounded by c.timeout: a server that never writes a response line
// (hung, deadlocked, or just slow) fails this one call instead of blocking
// the caller forever. The blocking read still runs to completion in its own
// goroutine after a timeout -- it has nothing left to hand its result to,
// but it will not leak past the point the pipe closes or the process exits,
// since the result channel is buffered and the goroutine exits once it
// sends to it.
func (c *StdioClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("escribiendo a servidor MCP: %w", err)
	}

	type readResult struct {
		line []byte
		err  error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		respLine, err := c.stdout.ReadBytes('\n')
		resultCh <- readResult{respLine, err}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, fmt.Errorf("leyendo respuesta del servidor MCP: %w", res.err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(res.line, &resp); err != nil {
			return nil, fmt.Errorf("respuesta MCP inesperada: %s", string(res.line))
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp error: %s", resp.Error.Message)
		}
		if resp.ID != id {
			return nil, fmt.Errorf("respuesta MCP para id %d recibida con id %d", id, resp.ID)
		}
		return resp.Result, nil
	case <-time.After(c.timeout):
		return nil, fmt.Errorf("el servidor MCP no respondio en %s (metodo %s)", c.timeout, method)
	}
}

// ListTools llama al método estándar "tools/list".
func (c *StdioClient) ListTools() (json.RawMessage, error) {
	return c.call("tools/list", nil)
}

// DiscoverTools decodes the standard tools/list envelope. Keeping the JSON
// parsing in this package means the agent does not need to know MCP wire
// details in order to register a server's tools.
func (c *StdioClient) DiscoverTools() ([]ToolDefinition, error) {
	raw, err := c.ListTools()
	if err != nil {
		return nil, err
	}
	var response struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("respuesta tools/list invalida: %w", err)
	}
	return response.Tools, nil
}

// CallTool llama a "tools/call" con el nombre y argumentos dados.
func (c *StdioClient) CallTool(name string, args map[string]any) (json.RawMessage, error) {
	return c.call("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
}

// Close asks the subprocess to exit by closing its stdin, then waits up to
// CloseGracePeriod before sending SIGKILL. A server that ignores stdin
// closing (or is simply deadlocked) must not be able to hang Loom's own
// shutdown -- an incident responder closing their session should never be
// stuck waiting on a misbehaving integration.
func (c *StdioClient) Close() error {
	_ = c.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(CloseGracePeriod):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-done // Wait always returns once the process is gone; don't leak the goroutine.
		return fmt.Errorf("el servidor MCP no salio tras cerrar stdin; se envio SIGKILL")
	}
}

// abort is used only while starting the process. Unlike Close, it does not
// grant a grace period: initialization has already failed or timed out and
// StartStdioServer must return promptly rather than leaving a child behind.
func (c *StdioClient) abort() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}
