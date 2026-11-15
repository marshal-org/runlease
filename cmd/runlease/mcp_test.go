package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the runlease CLI: with
// RUNLEASE_MCP_FAKE set it behaves as a tiny fake of it and exits, which is
// how the MCP tools are tested without a cloud. The server finds it through
// RUNLEASE_MCP_BIN, the same override a user could set.
func TestMain(m *testing.M) {
	if os.Getenv("RUNLEASE_MCP_FAKE") != "" {
		os.Exit(fakeRunlease(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeRunlease(args []string) int {
	last := args[len(args)-1]
	switch {
	case args[0] == "doctor":
		fmt.Fprintln(os.Stderr, `Error: unknown command "doctor" for "runlease"`)
		return 1
	case args[0] == "exec" && last == "sleep":
		time.Sleep(30 * time.Second)
	case args[0] == "exec" && last == "big":
		line := strings.Repeat("x", 99) + "\n"
		for i := range 3000 { // 300 KB
			if i == 2999 {
				line = "the-last-line\n"
			}
			fmt.Print(line)
		}
	case args[0] == "exec" && last == "fail":
		fmt.Fprintln(os.Stderr, "Error: instance is on fire\nPut it out, then retry")
		return 3
	default:
		b, _ := json.Marshal(args)
		fmt.Println(string(b))
	}
	return 0
}

// mcpClient drives a server in-process through a pair of pipes, the way an
// MCP client drives the real one over stdio.
type mcpClient struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	done chan error
}

func startMCP(t *testing.T) *mcpClient {
	t.Helper()
	t.Setenv("RUNLEASE_MCP_FAKE", "1")
	t.Setenv("RUNLEASE_MCP_BIN", os.Args[0])
	bin, err := mcpBinary()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &mcpServer{bin: bin, version: "v0.0.0-test", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c := &mcpClient{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	c.out.Buffer(nil, 1<<20)
	go func() {
		c.done <- s.serve(context.Background(), inR, outW)
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case err := <-c.done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not exit after stdin closed")
		}
	})
	return c
}

func (c *mcpClient) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

type mcpReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  map[string]any  `json:"result"`
	Error   *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	} `json:"error"`
}

func (c *mcpClient) read() mcpReply {
	c.t.Helper()
	if !c.out.Scan() {
		c.t.Fatalf("server closed its output: %v", c.out.Err())
	}
	var r mcpReply
	if err := json.Unmarshal(c.out.Bytes(), &r); err != nil {
		c.t.Fatalf("not a JSON message: %q: %v", c.out.Text(), err)
	}
	if r.JSONRPC != "2.0" {
		c.t.Fatalf("reply without jsonrpc 2.0: %q", c.out.Text())
	}
	return r
}

func (c *mcpClient) roundTrip(line string) mcpReply {
	c.t.Helper()
	c.send(line)
	return c.read()
}

// call runs a tool the legacy way and returns its text and isError.
func (c *mcpClient) call(tool, args string) (string, bool) {
	c.t.Helper()
	r := c.roundTrip(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args))
	if r.Error != nil {
		c.t.Fatalf("%s: JSON-RPC error %d %s", tool, r.Error.Code, r.Error.Message)
	}
	content := r.Result["content"].([]any)
	if len(content) != 1 {
		c.t.Fatalf("%s: want one content block, got %d", tool, len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		c.t.Fatalf("%s: content type %v", tool, block["type"])
	}
	return block["text"].(string), r.Result["isError"].(bool)
}

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

func TestMCP_legacyHandshake(t *testing.T) {
	c := startMCP(t)
	r := c.roundTrip(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}
	if got := r.Result["protocolVersion"]; got != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want the client's 2025-06-18", got)
	}
	if _, ok := r.Result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities lack tools: %v", r.Result["capabilities"])
	}
	info := r.Result["serverInfo"].(map[string]any)
	if info["name"] != "runlease" || info["version"] != "v0.0.0-test" {
		t.Errorf("serverInfo = %v", info)
	}
	if _, ok := r.Result["resultType"]; ok {
		t.Error("a legacy result should not carry resultType")
	}

	// A version this server does not speak is answered with its newest
	// legacy one, not an error.
	r = c.roundTrip(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01","capabilities":{}}}`)
	if got := r.Result["protocolVersion"]; got != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want 2025-11-25", got)
	}

	// Notifications get no reply: the next line out must answer the ping.
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(`{"jsonrpc":"2.0","method":"notifications/never-heard-of-it","params":{}}`)
	r = c.roundTrip(`{"jsonrpc":"2.0","id":"p1","method":"ping"}`)
	if string(r.ID) != `"p1"` || r.Error != nil || len(r.Result) != 0 {
		t.Errorf("ping reply = %+v, want an empty result for id p1", r)
	}
}

func TestMCP_modern(t *testing.T) {
	c := startMCP(t)
	r := c.roundTrip(`{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{` + modernMeta + `}}`)
	if r.Error != nil {
		t.Fatalf("server/discover: %+v", r.Error)
	}
	if r.Result["resultType"] != "complete" {
		t.Errorf("resultType = %v", r.Result["resultType"])
	}
	versions := fmt.Sprint(r.Result["supportedVersions"])
	if !strings.Contains(versions, "2026-07-28") || !strings.Contains(versions, "2025-11-25") {
		t.Errorf("supportedVersions = %s", versions)
	}
	meta := r.Result["_meta"].(map[string]any)
	if meta["io.modelcontextprotocol/serverInfo"].(map[string]any)["name"] != "runlease" {
		t.Errorf("_meta = %v", meta)
	}

	r = c.roundTrip(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{` + modernMeta + `}}`)
	if r.Result["resultType"] != "complete" || r.Result["cacheScope"] != "public" || r.Result["ttlMs"] == nil {
		t.Errorf("modern tools/list lacks resultType, cacheScope or ttlMs: %v", r.Result)
	}

	r = c.roundTrip(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"runlease_list","arguments":{},` + modernMeta + `}}`)
	if r.Result["resultType"] != "complete" || r.Result["isError"] != false {
		t.Errorf("modern tools/call = %v", r.Result)
	}

	r = c.roundTrip(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if r.Error == nil || r.Error.Code != -32022 || r.Error.Data["requested"] != "1900-01-01" || r.Error.Data["supported"] == nil {
		t.Errorf("unsupported version reply = %+v", r.Error)
	}

	r = c.roundTrip(`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	if r.Error == nil || r.Error.Code != -32602 {
		t.Errorf("missing clientCapabilities reply = %+v, want -32602", r.Error)
	}
}

func TestMCP_toolsList(t *testing.T) {
	c := startMCP(t)
	r := c.roundTrip(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if r.Error != nil {
		t.Fatalf("tools/list: %+v", r.Error)
	}
	want := map[string]bool{
		"runlease_up": false, "runlease_exec": false, "runlease_push": false, "runlease_pull": false, "runlease_list": false,
		"runlease_snooze": false, "runlease_down": false, "runlease_status": false, "runlease_doctor": false,
	}
	types := map[string]bool{"string": true, "boolean": true, "integer": true}
	for _, raw := range r.Result["tools"].([]any) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected tool %s", name)
		}
		want[name] = true
		if len(tool["description"].(string)) < 20 {
			t.Errorf("%s: description too short", name)
		}

		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: schema must be a closed object: %v", name, schema)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: properties is not an object", name)
		}
		for pname, praw := range props {
			p := praw.(map[string]any)
			if !types[fmt.Sprint(p["type"])] {
				t.Errorf("%s.%s: type %v", name, pname, p["type"])
			}
			if p["description"] == nil || p["description"] == "" {
				t.Errorf("%s.%s: no description", name, pname)
			}
		}
		if req, ok := schema["required"]; ok {
			for _, n := range req.([]any) {
				if _, ok := props[n.(string)]; !ok {
					t.Errorf("%s: required %q is not a property", name, n)
				}
			}
		}

		ann := tool["annotations"].(map[string]any)
		switch name {
		case "runlease_list", "runlease_status", "runlease_doctor":
			if ann["readOnlyHint"] != true || ann["destructiveHint"] != false {
				t.Errorf("%s: want read-only, got %v", name, ann)
			}
		case "runlease_down":
			if ann["destructiveHint"] != true || ann["readOnlyHint"] != false {
				t.Errorf("%s: want destructive, got %v", name, ann)
			}
		case "runlease_up", "runlease_snooze":
			if ann["destructiveHint"] != false || ann["readOnlyHint"] != false {
				t.Errorf("%s: want neither read-only nor destructive, got %v", name, ann)
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %s is missing", name)
		}
	}
	up := mcpTools[1].Description
	for _, must := range []string{"billed", "shortest lease", "NAT gateway", "ask the user", "Never set public_ip"} {
		if !strings.Contains(up, must) {
			t.Errorf("runlease_up description must mention %q", must)
		}
	}
}

func TestMCP_toolArgv(t *testing.T) {
	c := startMCP(t)
	cases := []struct {
		tool, args, want string
	}{
		{"runlease_list", `{}`, `["list","--output","json"]`},
		{"runlease_up", `{"provider":"gcp"}`, `["up","--detach","--output","json","--provider","gcp"]`},
		{"runlease_up", `{"provider":"aws","name":"box","size":"compute.large","ttl":"45m","region":"us-east-1","public_ip":true,"manifest":"./m.yaml"}`,
			`["up","--detach","--output","json","--provider","aws","--name","box","--size","compute.large","--ttl","45m","--region","us-east-1","--public-ip","--manifest","./m.yaml"]`},
		{"runlease_up", `{"provider":"gcp","public_ip":false}`, `["up","--detach","--output","json","--provider","gcp"]`},
		// The command is one argument, whatever is in it.
		{"runlease_exec", `{"name":"box","command":"echo 'a b'; ls -la | wc -l"}`,
			`["exec","--no-stdin","box","--","bash","-lc","echo 'a b'; ls -la | wc -l"]`},
		{"runlease_push", `{"name":"box","local_path":"a b.txt","remote_path":"/tmp/x"}`, `["push","--","box","a b.txt","/tmp/x"]`},
		{"runlease_pull", `{"name":"box","remote_path":"/tmp/x","local_path":"."}`, `["pull","--","box","/tmp/x","."]`},
		{"runlease_snooze", `{"name":"box","duration":"30m"}`, `["snooze","--output","json","--","box","30m"]`},
		{"runlease_down", `{"name":"box"}`, `["down","--yes","--","box"]`},
	}
	for _, tc := range cases {
		text, isErr := c.call(tc.tool, tc.args)
		if isErr {
			t.Errorf("%s %s: isError, text %q", tc.tool, tc.args, text)
		}
		if want := "exit code: 0\nstdout:\n" + tc.want; text != want {
			t.Errorf("%s %s:\n got %q\nwant %q", tc.tool, tc.args, text, want)
		}
	}

	text, isErr := c.call("runlease_status", `{}`)
	if isErr || !strings.Contains(text, `["whoami"]`) || !strings.Contains(text, `["list","--output","json"]`) {
		t.Errorf("runlease_status: isError=%v text=%q", isErr, text)
	}
}

func TestMCP_argumentValidation(t *testing.T) {
	c := startMCP(t)
	cases := []struct {
		tool, args, want string
	}{
		{"runlease_down", `{"name":"--yes"}`, "invalid machine name"},
		{"runlease_down", `{"name":"x; rm"}`, "invalid machine name"},
		{"runlease_down", `{"name":""}`, "invalid machine name"},
		{"runlease_down", `{}`, "invalid machine name"},
		{"runlease_down", `{"name":"box","force":true}`, "unknown field"},
		{"runlease_exec", `{"name":"-R","command":"id"}`, "invalid machine name"},
		{"runlease_exec", `{"name":"$(id)","command":"id"}`, "invalid machine name"},
		{"runlease_exec", `{"name":"box","command":" "}`, "command is required"},
		{"runlease_exec", `{"name":"box","command":"id","timeout_seconds":0}`, "invalid timeout_seconds"},
		{"runlease_exec", `{"name":"box","command":"id","timeout_seconds":3601}`, "invalid timeout_seconds"},
		{"runlease_exec", `{"name":"box","command":"id","timeout_seconds":1.5}`, "invalid timeout_seconds"},
		{"runlease_push", `{"name":"box","local_path":"--help","remote_path":"/tmp/x"}`, "may not start with '-'"},
		{"runlease_push", `{"name":"box","local_path":"a","remote_path":"-x"}`, "may not start with '-'"},
		{"runlease_pull", `{"name":"box","remote_path":"/tmp/x","local_path":""}`, "local_path is required"},
		{"runlease_snooze", `{"name":"box","duration":"soon"}`, "invalid duration"},
		{"runlease_snooze", `{"name":"box","duration":"0s"}`, "at least 1s"},
		{"runlease_snooze", `{"name":"--direct","duration":"1h"}`, "invalid machine name"},
		{"runlease_up", `{}`, "invalid provider"},
		{"runlease_up", `{"provider":"azure"}`, "invalid provider"},
		{"runlease_up", `{"provider":"gcp","name":"--attach"}`, "invalid machine name"},
		{"runlease_up", `{"provider":"gcp","ttl":"-m"}`, "invalid ttl"},
		{"runlease_up", `{"provider":"gcp","size":"--relay=evil"}`, "invalid size"},
		{"runlease_up", `{"provider":"gcp","region":"us east"}`, "invalid region"},
		{"runlease_up", `{"provider":"gcp","manifest":"-m"}`, "may not start with '-'"},
		{"runlease_up", `{"provider":"gcp","public_ip":"yes"}`, "invalid arguments"},
		{"runlease_doctor", `{"provider":"--x"}`, "invalid provider"},
		{"runlease_list", `{"all":true}`, "unknown field"},
	}
	for _, tc := range cases {
		text, isErr := c.call(tc.tool, tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s %s: isError=%v text=%q, want an error containing %q", tc.tool, tc.args, isErr, text, tc.want)
		}
		if strings.Contains(text, "exit code") {
			t.Errorf("%s %s: the CLI was run with invalid arguments: %q", tc.tool, tc.args, text)
		}
	}
}

func TestMCP_execUnknownMachine(t *testing.T) {
	t.Setenv("RUNLEASE_MCP_FAKE", "1")
	s := &mcpServer{bin: os.Args[0], log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		knownMachine: func(name string) error { return fmt.Errorf("instance %q not found", name) }}
	res := mcpRunExec(context.Background(), s, json.RawMessage(`{"name":"typo","command":"rm -rf build"}`))
	if !res.IsError || !strings.Contains(res.Text, `instance "typo" not found`) || !strings.Contains(res.Text, "runlease_list") {
		t.Errorf("want a not-found error and no run, got %+v", res)
	}
}

func TestMCP_failingCommandIsToolError(t *testing.T) {
	c := startMCP(t)
	text, isErr := c.call("runlease_exec", `{"name":"box","command":"fail"}`)
	if !isErr {
		t.Error("a failing command must set isError")
	}
	for _, want := range []string{"exit code: 3", "stderr:", "Error: instance is on fire", "Put it out, then retry"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q: %q", want, text)
		}
	}

	text, isErr = c.call("runlease_doctor", `{"provider":"gcp"}`)
	if !isErr || !strings.Contains(text, "has no doctor command") {
		t.Errorf("runlease_doctor on a build without it: isError=%v text=%q", isErr, text)
	}
}

func TestMCP_outputTruncation(t *testing.T) {
	c := startMCP(t)
	text, isErr := c.call("runlease_exec", `{"name":"box","command":"big"}`)
	if isErr {
		t.Fatalf("isError: %q", text[:200])
	}
	if len(text) > mcpStdoutCap+300 {
		t.Errorf("returned %d bytes, cap is %d", len(text), mcpStdoutCap)
	}
	if !strings.Contains(text, "[stdout truncated: the first ") {
		t.Errorf("no truncation notice in %q", text[:200])
	}
	if !strings.HasSuffix(text, "the-last-line") {
		t.Errorf("the tail was not kept: ...%q", text[len(text)-60:])
	}
}

func TestMCPTail(t *testing.T) {
	tail := mcpTail{max: 10}
	for _, chunk := range []string{"abc", "defgh", "ijklm", "n"} {
		tail.Write([]byte(chunk)) //nolint:errcheck
	}
	if got := tail.String(); got != "efghijklmn" || tail.total != 14 {
		t.Errorf("tail = %q total %d, want efghijklmn 14", got, tail.total)
	}
	tail.Write([]byte("0123456789ABCDEF")) //nolint:errcheck
	if got := tail.String(); got != "6789ABCDEF" {
		t.Errorf("tail after an oversized write = %q", got)
	}
}

func TestMCP_timeout(t *testing.T) {
	c := startMCP(t)
	start := time.Now()
	text, isErr := c.call("runlease_exec", `{"name":"box","command":"sleep","timeout_seconds":1}`)
	if !isErr || !strings.Contains(text, "timed out after 1s") || !strings.Contains(text, "may still be running") {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("timeout took %s", d)
	}
}

func TestMCP_cancel(t *testing.T) {
	c := startMCP(t)
	c.send(`{"jsonrpc":"2.0","id":41,"method":"tools/call","params":{"name":"runlease_exec","arguments":{"name":"box","command":"sleep"}}}`)
	// The server stays responsive while the call runs.
	if r := c.roundTrip(`{"jsonrpc":"2.0","id":42,"method":"ping"}`); string(r.ID) != "42" {
		t.Fatalf("want the ping answered while a call runs, got id %s", r.ID)
	}
	time.Sleep(200 * time.Millisecond) // let the subprocess start
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":41,"reason":"test"}}`)
	// A cancelled request gets no response, so the next reply is the ping's.
	// The cleanup then proves the server exits without waiting 30s for sleep.
	if r := c.roundTrip(`{"jsonrpc":"2.0","id":43,"method":"ping"}`); string(r.ID) != "43" {
		t.Fatalf("want no reply for the cancelled call, got id %s: %+v", r.ID, r)
	}
}

func TestMCP_protocolErrors(t *testing.T) {
	c := startMCP(t)
	cases := []struct {
		name, line string
		code       int
		id         string
	}{
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, -32601, "1"},
		{"unknown tool", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"runlease_nuke","arguments":{}}}`, -32602, "2"},
		{"no tool name", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`, -32602, "3"},
		{"arguments not an object", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"runlease_list","arguments":[1]}}`, -32602, "4"},
		{"not JSON", `{"jsonrpc":`, -32700, "null"},
		{"batch", `[{"jsonrpc":"2.0","id":5,"method":"ping"}]`, -32600, "null"},
		{"no method", `{"jsonrpc":"2.0","id":6}`, -32600, "6"},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":7,"method":"ping"}`, -32600, "null"},
		{"discover without _meta", `{"jsonrpc":"2.0","id":8,"method":"server/discover"}`, -32602, "8"},
	}
	for _, tc := range cases {
		r := c.roundTrip(tc.line)
		if r.Error == nil || r.Error.Code != tc.code || string(r.ID) != tc.id {
			t.Errorf("%s: got id %s error %+v, want id %s code %d", tc.name, r.ID, r.Error, tc.id, tc.code)
		}
	}
	// A stray response from the client is dropped, not answered.
	c.send(`{"jsonrpc":"2.0","id":9,"result":{}}`)
	if r := c.roundTrip(`{"jsonrpc":"2.0","id":10,"method":"ping"}`); string(r.ID) != "10" {
		t.Errorf("want the ping's reply next, got id %s", r.ID)
	}
}
