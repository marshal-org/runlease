package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The tools `runlease mcp` offers. Each one runs this binary as a subprocess
// with its own argv (never a shell), so the server shares no flag state with
// the commands it runs and a value from the model can only ever be one
// argument.

const (
	mcpStdoutCap = 100 << 10 // tail of stdout returned to the model
	mcpStderrCap = 16 << 10  // tail of stderr returned to the model

	mcpExecDefaultTimeout = 600  // seconds
	mcpExecMaxTimeout     = 3600 // seconds

	mcpQuickTimeout    = 2 * time.Minute
	mcpUpTimeout       = 10 * time.Minute
	mcpDownTimeout     = 5 * time.Minute
	mcpTransferTimeout = 30 * time.Minute
)

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations mcpAnnotations `json:"annotations"`

	run func(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult
}

// mcpAnnotations are the behaviour hints of the MCP schema. All four are
// always sent: the protocol's defaults (destructive, open world) are not what
// a reader would guess.
type mcpAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

type mcpToolResult struct {
	Text    string
	IsError bool
}

func mcpFail(format string, a ...any) mcpToolResult {
	return mcpToolResult{Text: fmt.Sprintf(format, a...), IsError: true}
}

func mcpSchema(required []string, props map[string]any) map[string]any {
	s := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func mcpString(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

const mcpNameDesc = "Machine name, as given to runlease_up and shown by runlease_list."

var mcpTools = []mcpTool{
	{
		Name:  "runlease_status",
		Title: "Runlease status",
		Description: "Show who is signed in to the relay, how many machine slots their tier has in use, and the machines " +
			"this computer tracks (as JSON: name, state, region, expiresAt, hourly price). Call this first.",
		InputSchema: mcpSchema(nil, map[string]any{}),
		Annotations: mcpAnnotations{ReadOnlyHint: true, OpenWorldHint: true},
		run:         mcpRunStatus,
	},
	{
		Name:  "runlease_up",
		Title: "Launch a machine",
		Description: "Launch a spot machine in the user's own cloud account and return its details as JSON without waiting for it to boot. " +
			"The machine is billed to the user's cloud account until its lease (ttl) ends, when it deletes itself: " +
			"pick the shortest lease that fits the job, and call runlease_down when done. " +
			"It takes about a minute before runlease_exec can reach a new machine; retry until it does. " +
			"If the result says the subnet has no NAT gateway, nothing was created: ask the user before retrying with " +
			"public_ip=true. Never set public_ip on your own.",
		InputSchema: mcpSchema([]string{"provider"}, map[string]any{
			"provider": map[string]any{"type": "string", "enum": []string{"gcp", "aws"}, "description": "Cloud to launch in."},
			"name":     mcpString("Name for the machine: letters, digits, '.', '_' and '-', starting with a letter or digit. Generated if omitted."),
			"size":     mcpString("Machine class, e.g. compute.small (default), compute.medium, compute.large, compute.xlarge, compute.2xlarge, compute.gpu."),
			"ttl":      mcpString("Lease length as a Go duration, e.g. 30m, 2h. The machine deletes itself when it ends. Default 2h (or the user's configured default)."),
			"region":   mcpString("Region or zone, e.g. us-central1-a (GCP) or us-east-1 (AWS). Defaults to the user's configured region."),
			"public_ip": map[string]any{"type": "boolean", "description": "Give the machine a public IP for outbound traffic. " +
				"Only set true after the user agreed, in answer to a no-NAT-gateway refusal."},
			"manifest": mcpString("Path to a manifest YAML on this computer. When set, the manifest describes the machine and name, size, ttl and region are ignored."),
		}),
		Annotations: mcpAnnotations{OpenWorldHint: true},
		run:         mcpRunUp,
	},
	{
		Name:  "runlease_exec",
		Title: "Run a command on a machine",
		Description: "Run a shell command on a machine as 'bash -lc <command>' and return its exit code, stdout and stderr. " +
			"No terminal and no stdin. Long output is cut to its last ~100 KB, and the result says so. " +
			"A command that outlives timeout_seconds is abandoned and may keep running on the machine: " +
			"for long jobs, start them in the background (nohup ... &) and poll.",
		InputSchema: mcpSchema([]string{"name", "command"}, map[string]any{
			"name":    mcpString(mcpNameDesc),
			"command": mcpString("Shell command line, run by bash -lc on the machine."),
			"timeout_seconds": map[string]any{
				"type": "integer", "minimum": 1, "maximum": mcpExecMaxTimeout, "default": mcpExecDefaultTimeout,
				"description": "How long to wait for the command, in seconds. Default 600, at most 3600.",
			},
		}),
		Annotations: mcpAnnotations{DestructiveHint: true, OpenWorldHint: true},
		run:         mcpRunExec,
	},
	{
		Name:        "runlease_push",
		Title:       "Copy a file to a machine",
		Description: "Copy one local file to a machine, replacing the remote file if it exists. Directories are not copied: archive them first.",
		InputSchema: mcpSchema([]string{"name", "local_path", "remote_path"}, map[string]any{
			"name":        mcpString(mcpNameDesc),
			"local_path":  mcpString("File on this computer. A relative path is relative to the directory the MCP server was started in."),
			"remote_path": mcpString("Destination file path on the machine."),
		}),
		Annotations: mcpAnnotations{DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true},
		run:         mcpRunPush,
	},
	{
		Name:        "runlease_pull",
		Title:       "Copy a file from a machine",
		Description: "Copy one file from a machine to this computer, replacing the local file if it exists. If local_path is a directory, the file keeps its name inside it.",
		InputSchema: mcpSchema([]string{"name", "remote_path", "local_path"}, map[string]any{
			"name":        mcpString(mcpNameDesc),
			"remote_path": mcpString("File on the machine."),
			"local_path":  mcpString("Destination on this computer. A relative path is relative to the directory the MCP server was started in."),
		}),
		Annotations: mcpAnnotations{DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true},
		run:         mcpRunPull,
	},
	{
		Name:        "runlease_list",
		Title:       "List machines",
		Description: "List the machines this computer tracks, as JSON: name, id, provider, state, region, expiresAt and hourly price.",
		InputSchema: mcpSchema(nil, map[string]any{}),
		Annotations: mcpAnnotations{ReadOnlyHint: true, OpenWorldHint: true},
		run: func(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
			if err := mcpDecode(args, &struct{}{}); err != nil {
				return mcpFail("%v", err)
			}
			return s.runCLI(ctx, mcpQuickTimeout, "list", "--output", "json").toolResult()
		},
	},
	{
		Name:  "runlease_snooze",
		Title: "Move a machine's lease deadline",
		Description: "Set a machine's self-deletion to duration from now (not added to the time left). " +
			"A longer lease costs the user more: extend only as far as the job needs.",
		InputSchema: mcpSchema([]string{"name", "duration"}, map[string]any{
			"name":     mcpString(mcpNameDesc),
			"duration": mcpString("Go duration from now until the machine deletes itself, e.g. 30m, 2h."),
		}),
		Annotations: mcpAnnotations{OpenWorldHint: true},
		run:         mcpRunSnooze,
	},
	{
		Name:        "runlease_down",
		Title:       "Delete a machine",
		Description: "Delete a machine now, with its disk and everything on it. This cannot be undone. Billing for it stops.",
		InputSchema: mcpSchema([]string{"name"}, map[string]any{"name": mcpString(mcpNameDesc)}),
		Annotations: mcpAnnotations{DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true},
		run:         mcpRunDown,
	},
	{
		Name:  "runlease_doctor",
		Title: "Check the setup",
		Description: "Check that this computer can launch machines (sign-in, cloud credentials, network) and report what to fix, as JSON. " +
			"Use it when runlease_up fails for a reason that is not clear.",
		InputSchema: mcpSchema(nil, map[string]any{
			"provider": map[string]any{"type": "string", "enum": []string{"gcp", "aws"}, "description": "Cloud to check. Defaults to the user's configured one."},
			"region":   mcpString("Region or zone to check. Defaults to the user's configured one."),
		}),
		Annotations: mcpAnnotations{ReadOnlyHint: true, OpenWorldHint: true},
		run:         mcpRunDoctor,
	},
}

var (
	// The CLI itself only requires a non-empty name. This is the subset every
	// cloud's naming can carry, and it shuts out anything that could be read
	// as a flag or a shell fragment.
	mcpNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	mcpSizeRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	mcpRegionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

func mcpCheckName(name string) error {
	if !mcpNameRe.MatchString(name) {
		return fmt.Errorf("invalid machine name %q: use letters, digits, '.', '_' and '-', starting with a letter or digit, at most 63 characters", name)
	}
	return nil
}

func mcpCheckPath(field, p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%s is required", field)
	case strings.HasPrefix(p, "-"):
		return fmt.Errorf("invalid %s %q: a path may not start with '-' (write ./%s for a file of that name)", field, p, p)
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("invalid %s: a path may not contain a NUL byte", field)
	}
	return nil
}

func mcpCheckDuration(field, d string) error {
	v, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("invalid %s %q: use a Go duration such as 30m or 2h", field, d)
	}
	if v < time.Second {
		return fmt.Errorf("invalid %s %q: it must be at least 1s", field, d)
	}
	return nil
}

// mcpDecode reads a tool's arguments, refusing ones the tool does not have:
// a misspelt argument should be reported, not dropped.
func mcpDecode(args json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}

func mcpRunStatus(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	if err := mcpDecode(args, &struct{}{}); err != nil {
		return mcpFail("%v", err)
	}
	who := s.runCLI(ctx, mcpQuickTimeout, "whoami")
	list := s.runCLI(ctx, mcpQuickTimeout, "list", "--output", "json")
	return mcpToolResult{
		Text: "== sign-in and tier (runlease whoami) ==\n" + who.text() +
			"\n\n== machines (runlease list --output json) ==\n" + list.text(),
		// Not being signed in is a status worth reporting, not a failed call.
		IsError: who.failed() && list.failed(),
	}
}

func mcpRunUp(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Size     string `json:"size"`
		TTL      string `json:"ttl"`
		Region   string `json:"region"`
		PublicIP bool   `json:"public_ip"`
		Manifest string `json:"manifest"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if a.Provider != "gcp" && a.Provider != "aws" {
		return mcpFail("invalid provider %q: use gcp or aws", a.Provider)
	}
	argv := []string{"up", "--detach", "--output", "json", "--provider", a.Provider}
	if a.Name != "" {
		if err := mcpCheckName(a.Name); err != nil {
			return mcpFail("%v", err)
		}
		argv = append(argv, "--name", a.Name)
	}
	if a.Size != "" {
		if !mcpSizeRe.MatchString(a.Size) {
			return mcpFail("invalid size %q: use a machine class such as compute.small", a.Size)
		}
		argv = append(argv, "--size", a.Size)
	}
	if a.TTL != "" {
		if err := mcpCheckDuration("ttl", a.TTL); err != nil {
			return mcpFail("%v", err)
		}
		argv = append(argv, "--ttl", a.TTL)
	}
	if a.Region != "" {
		if !mcpRegionRe.MatchString(a.Region) {
			return mcpFail("invalid region %q: use a region or zone such as us-central1-a or us-east-1", a.Region)
		}
		argv = append(argv, "--region", a.Region)
	}
	if a.PublicIP {
		argv = append(argv, "--public-ip")
	}
	if a.Manifest != "" {
		if err := mcpCheckPath("manifest", a.Manifest); err != nil {
			return mcpFail("%v", err)
		}
		argv = append(argv, "--manifest", a.Manifest)
	}
	return s.runCLI(ctx, mcpUpTimeout, argv...).toolResult()
}

func mcpRunExec(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Name    string   `json:"name"`
		Command string   `json:"command"`
		Timeout *float64 `json:"timeout_seconds"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if err := mcpCheckName(a.Name); err != nil {
		return mcpFail("%v", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return mcpFail("command is required")
	}
	secs := float64(mcpExecDefaultTimeout)
	if a.Timeout != nil {
		secs = *a.Timeout
		if secs != math.Trunc(secs) || secs < 1 || secs > mcpExecMaxTimeout {
			return mcpFail("invalid timeout_seconds %v: use a whole number from 1 to %d", secs, mcpExecMaxTimeout)
		}
	}
	// `runlease exec X -- cmd` treats X as part of the command when X is not a
	// machine it knows, and runs it on the only machine there is. A mistyped
	// name must not reach a different machine.
	if s.knownMachine != nil {
		if err := s.knownMachine(a.Name); err != nil {
			return mcpFail("%v. Use runlease_list to see the machines", err)
		}
	}
	timeout := time.Duration(secs) * time.Second
	r := s.runCLI(ctx, timeout, "exec", "--no-stdin", a.Name, "--", "bash", "-lc", a.Command)
	res := r.toolResult()
	if r.timedOut {
		res.Text += "\nThe command may still be running on the machine."
	}
	return res
}

func mcpRunPush(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Name   string `json:"name"`
		Local  string `json:"local_path"`
		Remote string `json:"remote_path"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if err := errors.Join(mcpCheckName(a.Name), mcpCheckPath("local_path", a.Local), mcpCheckPath("remote_path", a.Remote)); err != nil {
		return mcpFail("%v", err)
	}
	return s.runCLI(ctx, mcpTransferTimeout, "push", "--", a.Name, a.Local, a.Remote).toolResult()
}

func mcpRunPull(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Name   string `json:"name"`
		Remote string `json:"remote_path"`
		Local  string `json:"local_path"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if err := errors.Join(mcpCheckName(a.Name), mcpCheckPath("remote_path", a.Remote), mcpCheckPath("local_path", a.Local)); err != nil {
		return mcpFail("%v", err)
	}
	return s.runCLI(ctx, mcpTransferTimeout, "pull", "--", a.Name, a.Remote, a.Local).toolResult()
}

func mcpRunSnooze(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Name     string `json:"name"`
		Duration string `json:"duration"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if err := errors.Join(mcpCheckName(a.Name), mcpCheckDuration("duration", a.Duration)); err != nil {
		return mcpFail("%v", err)
	}
	return s.runCLI(ctx, mcpQuickTimeout, "snooze", "--output", "json", "--", a.Name, a.Duration).toolResult()
}

func mcpRunDown(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Name string `json:"name"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	if err := mcpCheckName(a.Name); err != nil {
		return mcpFail("%v", err)
	}
	return s.runCLI(ctx, mcpDownTimeout, "down", "--yes", "--", a.Name).toolResult()
}

func mcpRunDoctor(ctx context.Context, s *mcpServer, args json.RawMessage) mcpToolResult {
	var a struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}
	if err := mcpDecode(args, &a); err != nil {
		return mcpFail("%v", err)
	}
	argv := []string{"doctor", "--output", "json"}
	if a.Provider != "" {
		if a.Provider != "gcp" && a.Provider != "aws" {
			return mcpFail("invalid provider %q: use gcp or aws", a.Provider)
		}
		argv = append(argv, "--provider", a.Provider)
	}
	if a.Region != "" {
		if !mcpRegionRe.MatchString(a.Region) {
			return mcpFail("invalid region %q: use a region or zone such as us-central1-a or us-east-1", a.Region)
		}
		argv = append(argv, "--region", a.Region)
	}
	r := s.runCLI(ctx, mcpQuickTimeout, argv...)
	if r.failed() && strings.Contains(r.stderr.String(), `unknown command "doctor"`) {
		return mcpFail("this build of runlease (%s) has no doctor command. Upgrade runlease, or use runlease_status and the error text of the failing tool instead", s.version)
	}
	return r.toolResult()
}

// mcpCLIResult is what one run of the runlease binary produced.
type mcpCLIResult struct {
	argv0    string // the subcommand, for messages
	exit     int
	stdout   mcpTail
	stderr   mcpTail
	timeout  time.Duration
	timedOut bool
	startErr error
}

func (r *mcpCLIResult) failed() bool { return r.exit != 0 || r.timedOut || r.startErr != nil }

// text is the result as the model reads it: the exit code, then stdout, then
// stderr when there is any. The CLI's own error text is on stderr and already
// says what to do next.
func (r *mcpCLIResult) text() string {
	var b strings.Builder
	switch {
	case r.startErr != nil:
		fmt.Fprintf(&b, "could not run runlease %s: %v\n", r.argv0, r.startErr)
	case r.timedOut:
		fmt.Fprintf(&b, "timed out after %s: runlease %s was stopped before it finished\n", r.timeout, r.argv0)
	default:
		fmt.Fprintf(&b, "exit code: %d\n", r.exit)
	}
	for _, s := range []struct {
		name string
		t    *mcpTail
	}{{"stdout", &r.stdout}, {"stderr", &r.stderr}} {
		if s.t.total == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", s.name)
		if dropped := s.t.total - int64(len(s.t.buf)); dropped > 0 {
			fmt.Fprintf(&b, "[%s truncated: the first %d of %d bytes were dropped, the last %d follow]\n",
				s.name, dropped, s.t.total, len(s.t.buf))
		}
		out := strings.ToValidUTF8(s.t.String(), "\uFFFD")
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *mcpCLIResult) toolResult() mcpToolResult {
	return mcpToolResult{Text: r.text(), IsError: r.failed()}
}

// runCLI runs the runlease binary with argv and waits for it, for at most
// timeout. Cancelling ctx kills it.
func (s *mcpServer) runCLI(ctx context.Context, timeout time.Duration, argv ...string) *mcpCLIResult {
	r := &mcpCLIResult{argv0: argv[0], timeout: timeout}
	r.stdout.max, r.stderr.max = mcpStdoutCap, mcpStderrCap

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.bin, argv...)
	cmd.Stdin = nil // /dev/null: nothing may read the protocol stream
	cmd.Stdout = &r.stdout
	cmd.Stderr = &r.stderr
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.timedOut = true
	case errors.As(err, &exitErr):
		r.exit = exitErr.ExitCode()
	default:
		r.startErr = err
	}
	s.log.Info("tool ran", "command", argv[0], "exit", r.exit, "timedOut", r.timedOut,
		"cancelled", ctx.Err() != nil, "seconds", time.Since(start).Round(time.Millisecond).Seconds())
	return r
}

// mcpTail is an io.Writer that keeps only the last max bytes written to it,
// so a command that prints gigabytes cannot exhaust memory or the model's
// context.
type mcpTail struct {
	max   int
	buf   []byte
	total int64
}

func (t *mcpTail) Write(p []byte) (int, error) {
	t.total += int64(len(p))
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return len(p), nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[over:])]
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *mcpTail) String() string { return string(t.buf) }
