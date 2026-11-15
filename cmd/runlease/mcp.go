package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The Model Context Protocol server: `runlease mcp` speaks JSON-RPC 2.0 over
// stdin/stdout, one message per line, and exposes the CLI as typed tools.
//
// It serves both eras of the protocol on the same stream, as the 2026-07-28
// revision allows ("dual-era" server):
//
//   - modern (2026-07-28): no handshake. Every request names its protocol
//     version and client capabilities in params._meta, and results carry
//     resultType. server/discover advertises versions and capabilities.
//   - legacy (2025-11-25 and earlier): the client opens with initialize and
//     notifications/initialized, and may send ping.
//
// A request is modern when its _meta names a protocol version, legacy
// otherwise.

const (
	mcpModernVersion = "2026-07-28"

	mcpMetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	mcpMetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	mcpMetaServerInfo         = "io.modelcontextprotocol/serverInfo"

	// JSON-RPC and MCP error codes.
	rpcParseError         = -32700
	rpcInvalidRequest     = -32600
	rpcMethodNotFound     = -32601
	rpcInvalidParams      = -32602
	rpcUnsupportedVersion = -32022

	// mcpMaxLine bounds one incoming message. Tool arguments are short; the
	// largest legitimate one is a shell command.
	mcpMaxLine = 4 << 20

	// mcpToolsTTLMs tells a modern client how long the tool list stays fresh.
	// It only changes with the binary.
	mcpToolsTTLMs = 300000
)

// mcpLegacyVersions are the handshake-based revisions this server answers
// initialize for, newest first.
var mcpLegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = "Runlease launches short-lived spot machines in the user's own cloud account (GCP or AWS). " +
	"Each machine is billed to the user until its lease ends, and then deletes itself. " +
	"Start with runlease_status to see sign-in and existing machines. Launch with runlease_up using the shortest lease " +
	"that fits the job, run commands with runlease_exec, copy files with runlease_push and runlease_pull, extend a lease " +
	"with runlease_snooze, and delete the machine with runlease_down as soon as the work is done. " +
	"A new machine needs about a minute before runlease_exec can reach it; retry if the first call fails."

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve runlease to coding agents as a Model Context Protocol server (stdio)",
	Long: `Run a Model Context Protocol (MCP) server on stdin/stdout, so a coding agent
gets typed tools for leased machines instead of composing shell commands:

  runlease_status   sign-in, tier slots and machines in one call
  runlease_up       launch a spot machine with a lease
  runlease_exec     run a shell command on a machine
  runlease_push     copy a local file to a machine
  runlease_pull     copy a file from a machine
  runlease_list     list machines
  runlease_snooze   move a machine's lease deadline
  runlease_down     delete a machine
  runlease_doctor   check that this computer is set up to launch

Register it with an MCP client. The client starts this command itself; you do
not run it by hand.

  Claude Code:
    claude mcp add runlease -- runlease mcp

  Any MCP client (the usual "mcpServers" entry):
    {"mcpServers": {"runlease": {"command": "runlease", "args": ["mcp"]}}}

The server uses the same sign-in, config and cloud credentials as the CLI, so
'runlease login' and your cloud credentials must already work in a terminal.
Machines launched through it are billed to your own cloud account until their
lease ends. Each tool runs this same binary as a subprocess; logs go to stderr.

More: docs/mcp.md`,
	Args: cobra.NoArgs,
	RunE: runMCP,
}

func init() { rootCmd.AddCommand(mcpCmd) }

func runMCP(cmd *cobra.Command, _ []string) error {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(cmd.ErrOrStderr(), "runlease mcp is a server for MCP clients: it reads JSON-RPC from stdin. "+
			"Register it with 'claude mcp add runlease -- runlease mcp' (see 'runlease mcp --help'). Ctrl+D to exit.")
	}
	bin, err := mcpBinary()
	if err != nil {
		return err
	}
	s := &mcpServer{
		bin:     bin,
		version: version,
		log:     slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil)),
		knownMachine: func(name string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			_, err = reg.Get(name)
			return err
		},
	}
	return s.serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
}

// mcpBinary is the runlease binary the tools run: this one, unless
// RUNLEASE_MCP_BIN names another (tests point it at a fake).
func mcpBinary() (string, error) {
	if b := os.Getenv("RUNLEASE_MCP_BIN"); b != "" {
		return b, nil
	}
	b, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the runlease binary to run tools with: %w", err)
	}
	return b, nil
}

type mcpServer struct {
	bin     string
	version string
	log     *slog.Logger
	// knownMachine reports whether name is a machine this computer tracks.
	// nil skips the check.
	knownMachine func(name string) error

	out io.Writer
	wmu sync.Mutex // one message per line: writes must not interleave

	mu       sync.Mutex
	inflight map[string]context.CancelCauseFunc // tools/call requests by JSON-RPC id
	wg       sync.WaitGroup
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var (
	errMCPCancelled = errors.New("cancelled by the client")
	errMCPShutdown  = errors.New("the server is shutting down")
)

// serve reads messages until in reaches end-of-file or ctx ends.
func (s *mcpServer) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	s.inflight = make(map[string]context.CancelCauseFunc)

	ctx, stop := context.WithCancelCause(ctx)
	defer func() {
		stop(errMCPShutdown)
		s.wg.Wait()
	}()

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64<<10), mcpMaxLine)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			select {
			case lines <- bytes.Clone(line):
			case <-ctx.Done():
				readErr <- nil
				return
			}
		}
		readErr <- sc.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				// Stdin closed: the client is leaving. Calls already running
				// finish and are answered (each is bounded by its timeout): a
				// launch killed halfway is worse than a slow exit. A signal
				// ends ctx and stops them at once.
				idle := make(chan struct{})
				go func() { s.wg.Wait(); close(idle) }()
				select {
				case <-idle:
				case <-ctx.Done():
				}
				if err := <-readErr; err != nil {
					return fmt.Errorf("reading stdin: %w", err)
				}
				return nil
			}
			s.handle(ctx, line)
		}
	}
}

func (s *mcpServer) handle(ctx context.Context, line []byte) {
	if line[0] == '[' {
		s.replyError(json.RawMessage("null"), rpcInvalidRequest, "Invalid request: batches are not supported", nil)
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		s.replyError(json.RawMessage("null"), rpcParseError, "Parse error: "+err.Error(), nil)
		return
	}
	if msg.Method == "" {
		// A response (clients must not send those) or junk. Only something
		// that looks like a request gets an answer.
		if len(msg.ID) > 0 && msg.Result == nil && msg.Error == nil {
			s.replyError(msg.ID, rpcInvalidRequest, "Invalid request: no method", nil)
		}
		return
	}
	if len(msg.ID) == 0 {
		s.notification(msg)
		return
	}
	if string(msg.ID) == "null" || msg.JSONRPC != "2.0" {
		s.replyError(json.RawMessage("null"), rpcInvalidRequest, `Invalid request: need "jsonrpc": "2.0" and a string or number id`, nil)
		return
	}

	modern, rerr := mcpEra(msg.Params)
	if rerr != nil {
		s.reply(rpcResponse{ID: msg.ID, Error: rerr})
		return
	}

	switch msg.Method {
	case "initialize":
		s.reply(rpcResponse{ID: msg.ID, Result: s.initialize(msg.Params)})
	case "ping":
		// Removed in 2026-07-28, still answered: it costs nothing.
		s.reply(rpcResponse{ID: msg.ID, Result: s.result(modern, map[string]any{})})
	case "server/discover":
		if !modern {
			s.replyError(msg.ID, rpcInvalidParams, "Invalid params: server/discover needs _meta."+mcpMetaProtocolVersion, nil)
			return
		}
		s.reply(rpcResponse{ID: msg.ID, Result: s.result(true, map[string]any{
			"supportedVersions": s.supportedVersions(),
			"capabilities":      mcpCapabilities(),
			"instructions":      mcpInstructions,
			"ttlMs":             mcpToolsTTLMs,
			"cacheScope":        "public",
		})})
	case "tools/list":
		res := map[string]any{"tools": mcpTools}
		if modern {
			res["ttlMs"] = mcpToolsTTLMs
			res["cacheScope"] = "public"
		}
		s.reply(rpcResponse{ID: msg.ID, Result: s.result(modern, res)})
	case "tools/call":
		s.callTool(ctx, msg, modern)
	default:
		s.replyError(msg.ID, rpcMethodNotFound, "Method not found: "+msg.Method, nil)
	}
}

func (s *mcpServer) notification(msg rpcMessage) {
	if msg.Method != "notifications/cancelled" {
		return // notifications/initialized and anything else: nothing to do, never a reply
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	s.mu.Lock()
	cancel := s.inflight[mcpIDKey(p.RequestID)]
	s.mu.Unlock()
	if cancel != nil {
		s.log.Info("request cancelled", "id", mcpIDKey(p.RequestID), "reason", p.Reason)
		cancel(errMCPCancelled)
	}
}

// callTool validates a tools/call request and runs the tool on its own
// goroutine, so a long command does not hold up pings, other calls or the
// notification that cancels it.
func (s *mcpServer) callTool(ctx context.Context, msg rpcMessage, modern bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.Name == "" {
		s.replyError(msg.ID, rpcInvalidParams, "Invalid params: tools/call needs a tool name", nil)
		return
	}
	i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == p.Name })
	if i < 0 {
		s.replyError(msg.ID, rpcInvalidParams, "Unknown tool: "+p.Name, nil)
		return
	}
	tool := mcpTools[i]
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	if args[0] != '{' {
		s.replyError(msg.ID, rpcInvalidParams, "Invalid params: arguments must be an object", nil)
		return
	}

	key := mcpIDKey(msg.ID)
	callCtx, cancel := context.WithCancelCause(ctx)
	s.mu.Lock()
	if _, dup := s.inflight[key]; dup {
		s.mu.Unlock()
		cancel(nil)
		s.replyError(msg.ID, rpcInvalidRequest, "Invalid request: id is already in use by a running request", nil)
		return
	}
	s.inflight[key] = cancel
	s.mu.Unlock()

	s.wg.Go(func() {
		res := tool.run(callCtx, s, args)
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		cause := context.Cause(callCtx)
		cancel(nil)
		if cause != nil {
			// Cancelled, or stdin closed: the client is not waiting for this
			// answer, and must not be sent one.
			return
		}
		out := map[string]any{
			"content": []map[string]string{{"type": "text", "text": res.Text}},
			"isError": res.IsError,
		}
		s.reply(rpcResponse{ID: msg.ID, Result: s.result(modern, out)})
	})
}

// mcpEra reports whether a request is a modern one (it names a protocol
// version in _meta), and rejects a modern request this server cannot serve.
func mcpEra(params json.RawMessage) (modern bool, rerr *rpcError) {
	if len(params) == 0 {
		return false, nil
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return false, &rpcError{Code: rpcInvalidParams, Message: "Invalid params: params must be an object"}
	}
	raw, ok := p.Meta[mcpMetaProtocolVersion]
	if !ok {
		return false, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, &rpcError{Code: rpcInvalidParams, Message: "Invalid params: _meta." + mcpMetaProtocolVersion + " must be a string"}
	}
	if v != mcpModernVersion {
		return false, &rpcError{
			Code:    rpcUnsupportedVersion,
			Message: "Unsupported protocol version",
			Data: map[string]any{
				"supported": append([]string{mcpModernVersion}, mcpLegacyVersions...),
				"requested": v,
			},
		}
	}
	if _, ok := p.Meta[mcpMetaClientCapabilities]; !ok {
		return false, &rpcError{Code: rpcInvalidParams, Message: "Invalid params: missing _meta." + mcpMetaClientCapabilities}
	}
	return true, nil
}

// initialize answers the legacy handshake: the client's version when this
// server speaks it, otherwise the newest legacy version it does speak.
func (s *mcpServer) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	v := mcpLegacyVersions[0]
	if slices.Contains(mcpLegacyVersions, p.ProtocolVersion) {
		v = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": v,
		"capabilities":    mcpCapabilities(),
		"serverInfo":      s.serverInfo(),
		"instructions":    mcpInstructions,
	}
}

func (s *mcpServer) supportedVersions() []string {
	return append([]string{mcpModernVersion}, mcpLegacyVersions...)
}

func mcpCapabilities() map[string]any {
	return map[string]any{"tools": map[string]any{}}
}

func (s *mcpServer) serverInfo() map[string]string {
	return map[string]string{"name": "runlease", "title": "Runlease", "version": s.version}
}

// result adds what every modern result carries: resultType and the server's
// identity. Legacy results are returned as they are.
func (s *mcpServer) result(modern bool, res map[string]any) map[string]any {
	if modern {
		res["resultType"] = "complete"
		res["_meta"] = map[string]any{mcpMetaServerInfo: s.serverInfo()}
	}
	return res
}

func (s *mcpServer) replyError(id json.RawMessage, code int, message string, data any) {
	s.reply(rpcResponse{ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}

func (s *mcpServer) reply(r rpcResponse) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r) // Marshal never emits a raw newline
	if err != nil {
		s.log.Error("encoding a response", "err", err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		s.log.Error("writing a response", "err", err)
	}
}

// mcpIDKey is a request id in the one spelling used to match a cancellation
// to its request.
func mcpIDKey(id json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, id); err != nil {
		return string(id)
	}
	return b.String()
}
