# Changelog

All notable changes to this project will be documented here.

---

## [0.0.6] — 2026-05-28

### Added
- `cmd/runlease/manifest.go`: `runlease manifest new` — renamed from `manifest init` (kept as an alias for back-compat). Default output filename is now `<instance-name>-manifest.yaml` (derived from the entered instance name) instead of the always-collide-prone `manifest.yaml`. `-o/--output` still overrides. Ctrl+C during the wizard now exits cleanly with `Cancelled.` (was previously swallowed by the blocking `scanner.Scan()`) — mirrors the existing signal handling in `runlease configure`
- `cmd/runlease/manifest.go`: `runlease manifest schema` — prints the full annotated manifest schema (every field, every default, with inline comments) to stdout. Lets users discover the schema from the installed binary without needing the source checkout. Pipe to a file to start hand-editing: `runlease manifest schema > my-runner-manifest.yaml`
- `cmd/runlease/manifest.go`: wizard now prompts for `Sudo (locked/passwordless)` after the relay prompt; `passwordless` writes `sudo: passwordless` into the generated manifest. Locked stays the default
- `docs/manifest.md`: new field-by-field reference for the manifest schema (useful when browsing the repo on GitHub; CLI users should prefer `runlease manifest schema`)
- `cmd/runlease/exec.go`: new `runlease exec [name] -- <cmd>` — runs a command on the instance over the existing SSH/relay tunnel without allocating a PTY; streams stdout/stderr, forwards stdin (unless `--no-stdin`), propagates the remote exit code. Prefers relay when one is configured, `-R/--relay` forces relay over direct SSH
- `cmd/runlease/forward.go`: new `runlease forward [name] -L/-R …` — OpenSSH-style port forwards over the SSH/relay tunnel. Local forwards listen on the dev machine and dial out to the instance; remote forwards listen on the instance and dial back. `--all` applies every entry in `manifest.forwards` from the registry. Blocks until Ctrl+C
- `cmd/runlease/transfer.go`: new `runlease push [name] <local> <remote>` / `runlease pull [name] <remote> <local>` — file transfer over the SSH/relay tunnel via SFTP (`github.com/pkg/sftp`). Mirrors `scp` UX: pulling into an existing local directory uses the remote basename; push preserves file mode
- `cmd/runlease/attach.go`: new `runlease up --attach` mode — after provisioning, waits for the SSH/relay tunnel, tails `/var/log/runlease-setup.log` until the `__RUNLEASE_SETUP_DONE__` marker appears, then applies every `manifest.forwards` entry and blocks until Ctrl+C. One command for the developer-flow happy path (launch → setup → forwards live)
- `cmd/runlease/discover.go`: `runlease up` (no `-m` and no `--provider`) walks up from `cwd` looking for `runlease.yaml` (or `.yml`) and uses the first one it finds. Walk stops at filesystem root or `$HOME`
- `cmd/runlease/root.go`: `--output json` persistent flag — wires through `up`, `list`, and `exec` for CI/scripting. Stable JSON schemas; `exec` writes its `{command, exitCode}` record to stderr so stdout stays clean
- `pkg/manifest/manifest.go`: `sudo: passwordless` — opt-in NOPASSWD: ALL for the `runlease-agent` user via `/etc/sudoers.d/runlease-agent-full`. Default `locked` keeps the existing security posture (only `/usr/local/bin/runlease-terminate` is sudo-allowed)
- `pkg/manifest/manifest.go`: `setup:` — list of `run:` / `file:` scripts that execute as root after the runlease bootstrap completes. Output streams to `/var/log/runlease-setup.log`; `/run/runlease-setup-done` is touched on success. `file:` references are inlined at manifest-load time relative to the manifest directory
- `pkg/manifest/manifest.go`: `forwards:` — declarative list of port-forward entries, persisted to the registry at `up` time so `forward --all` and `up --attach` re-apply them without re-reading the manifest
- `pkg/registry/registry.go`: `ForwardSpec` — registry-side copy of `manifest.ForwardSpec` (kept here to avoid a registry → manifest import); stored on `Entry.Forwards`
- `pkg/agent/agent.go`: new package — extracts the agent core loop (dial relay, register, yamux server, accept streams) from `cmd/runlease-agent/main.go` so it can be exercised by integration tests and reused by future tooling
- `cmd/runlease-agent`: `RUNLEASE_LOCAL_TARGET` env var (defaults to `localhost:22`) — TCP target each yamux stream is bridged to. Enables in-process testing and future bridging to non-standard sshd ports

### Changed
- `cmd/runlease/up.go`, `cmd/runlease/connect.go`: `runlease up -m <manifest>` (interactive mode, default) and `runlease connect <name>` now auto-apply `manifest.forwards` alongside the shell. Forwards run in a goroutine on the same SSH client; they're torn down when the shell exits. Yamux makes this concurrent-safe — the relay supports multiple client streams over one agent socket. `runlease up --detach` still exits immediately (no shell, no forwards), and `runlease up --attach` continues to be the no-shell-just-forwards mode
- `cmd/runlease/ssh.go`: refactored `sshOverConn` to delegate client setup to the existing `newSSHClient` and extracted the PTY+shell logic into `sshInteractiveSession`, so the new with-forwards path can reuse it
- `pkg/userdata/userdata.go`: the `runlease-terminate` script + `/etc/sudoers.d/runlease-terminate` drop-in are now installed on **every** VM regardless of `reclaim.selfTermination`. The flag now only gates the auto-fire `systemd-run` timer. Rationale: the script is a small operator convenience (`sudo runlease-terminate` cleanly shuts down the VM from the inside) and there's no good reason to make it opt-in. Strictly additive — no existing behavior removed
- `pkg/relay/server.go`: replaced the 1:1 pair-once bridge with a multiplexed agent connection. The agent runs `yamux.Server` on its single outbound WebSocket; the relay runs `yamux.Client` and `OpenStream()`s per incoming CLI client. The CLI sees an unchanged byte stream — yamux is terminated by the relay against the agent's socket. Concurrent CLI processes (e.g. `forward` holding a tunnel while another shell runs `exec`/`push`/`pull`) all work against one VM through one agent socket. yamux keepalives stay enabled — the WebSocket layer doesn't ping, so NAT timeouts could otherwise silently drop idle outbound sessions
- `pkg/relay/server.go`: dropped the `paired` / `ready` handshake messages (legacy 1:1 contract); coordinator now sets `instanceReady` as soon as the yamux session opens. Dropped the unused `instanceIdleLimit` constant
- `pkg/userdata/userdata.go`: setup scripts (when `manifest.setup` is set) write to `/var/lib/runlease/setup/NN-name.sh`, execute with output `tee`'d to `/var/log/runlease-setup.log`, and emit `__RUNLEASE_SETUP_DONE__` to the log before touching `/run/runlease-setup-done` — so `--attach` and other tooling can poll for completion
- `pkg/userdata/userdata.go`: passwordless sudo (when `manifest.sudo: passwordless`) writes `/etc/sudoers.d/runlease-agent-full` with `runlease-agent ALL=(ALL) NOPASSWD: ALL`
- `cmd/runlease-agent/main.go`: slimmed to a thin reconnect loop on top of `pkg/agent.RunOnce`
- `pkg/relay/server_test.go`: rewritten for the yamux flow; added `TestRelay_concurrentClients` exercising N concurrent client websockets through one agent yamux session

### Fixed
- `cmd/runlease/attach.go`: `markerWriter` now signals a non-blocking channel when the setup-done marker is first seen, and `tailSetup` selects on `(ctx.Done, marker, copy-end)` with deferred tail teardown. Previously `io.Copy` on `tail -F` never returned on its own, so attach would block forever after setup completed even though the marker had been received

## [0.0.4] — 2026-05-05

### Added
- `cmd/runlease/sync.go`: new `runlease sync` command — queries each provider for current instance status, removes terminated/stopped entries from the registry, and prints explicit errors for any failures
- `cmd/runlease/list.go`: footnote after the table: `"State is cached — run 'runlease sync' to refresh"`
- `cmd/runlease/up.go`: `--self-terminate` flag (default `true`) for quick mode; `SelfTermination` wired through to manifest, userdata, and provider spec
- `pkg/manifest/manifest.go`: `ReclaimPolicy.SelfTermination bool` field (`selfTermination` in YAML)
- `pkg/provider/interface.go`: `SelfTermination bool` added to `InstanceSpec`
- `pkg/registry/registry.go`: `UpdateState(nameOrID, status)` method

### Changed
- `pkg/userdata/userdata.go`: `runlease-terminate` script is now gated on `SelfTermination: true` (was always installed); GCP default SA with compute scope is only attached when `SelfTermination` is set
- `pkg/provider/gcp/provider.go`: `generateInstanceName` strips leading `rl-` from manifest name before prepending `rl-`, preventing `rl-rl-` double prefix

### Fixed
- `pkg/provider/aws/provider.go`: `Terminate()` treats `InvalidInstanceID.NotFound` as success (instance already gone)
- `cmd/runlease/connect.go`: `close 1000` on first connect (agent mid-reconnect) falls back to `pollAndConnectViaRelay` instead of returning an error
- `pkg/userdata/userdata.go` (GCP self-terminate): replaced `python3` JSON parsing with `grep -o`/`cut`; added `|| systemctl --force --force poweroff` fallback on failed DELETE curl; removed bare `set -e` that caused silent exits

## [0.0.3] — 2026-05-04

### Added
- `pkg/provider/aws`: `ResolveAL2023AMI` — auto-detects the latest Amazon Linux 2023 AMI for the target region via `DescribeImages`; `--image` is no longer required for AWS launches
- `pkg/provider/aws`: `Name` tag added to instances so they appear labelled in the AWS console
- `pkg/provider/gcp`: `gcpLabels` + `gcpSanitize` — sanitizes arbitrary tag values to valid GCP label format (`[a-z0-9_-]`, ≤63 chars) before setting `inst.Labels`
- `cmd/runlease/up.go`: `mergeAutoTags` — automatically injects `runlease-managed=true`, `runlease-created-by=<user>`, and `runlease-expires-at=<RFC3339>` (when TTL set); user-defined tags take precedence
- `cmd/runlease/up.go`: post-launch termination instructions block (manual `runlease down`, on-instance `runlease-terminate`, and TTL countdown)
- `cmd/runlease/up.go`: emoji icons throughout launch output
- `cmd/runlease/down.go`: 0-arg invocation — auto-selects when one instance running; shows pick-list when multiple; interactive `[y/N]` confirmation prompt; `-y/--yes` flag to skip
- `cmd/runlease/connect.go`: auto-reregisters relay token when relay restarted (slot missing → "unauthorized" → re-register + poll)
- `cmd/runlease/config.go`: post-configure output now shows YAML block between separators, launch examples, relay tip, and `runlease manifest init` pointer for advanced options
- `cmd/runlease/manifest.go`: AWS `baseImage` prompt notes auto-detection; empty baseImage renders as a comment in generated YAML
- `pkg/userdata/userdata.go`: `runlease-terminate` script always installed (not gated on TTL); both GCP and AWS scripts are self-elevating (`[ "$EUID" -ne 0 ] && exec sudo "$0" "$@"`); `systemd-run` timer is conditional on `TTLSeconds > 0`

### Fixed
- `pkg/provider/gcp`: `resolveProjectID` uses `exec.LookPath("gcloud")` — distinct "not installed" vs "project not set" error messages
- `pkg/provider/gcp`, `cmd/runlease/up.go` (`googleIDToken`): graceful handling when `gcloud` or `aws` CLI is absent
- `pkg/manifest/manifest.go`: AWS manifests no longer require `baseImage` in validation (auto-detected at launch)
- `pkg/manifest/manifest_test.go`: "no baseImage" negative test now targets GCP provider to remain a valid failure case

## [0.0.2] — 2026-05-02

### Added
- `runlease up` quick mode: launch without a manifest using flags (`--provider`, `--region`, `--size`, `--image`, `--relay`, `--ttl`, `--name`) or config defaults
- `runlease manifest init` — interactive wizard that generates a manifest YAML with provider-specific hints
- `pkg/config` — `~/.runlease/config.yaml` with `Load`, `Save`, and dot-notation `Set`
- `runlease configure` — interactive setup wizard for provider, region, size, TTL, relay defaults
- `runlease config show` / `runlease config set <key> <value>` — non-interactive config management
- `publicIp` manifest field (default `true`); Cloud NAT note shown when launching GCP instance without public IP
- Relay `GET /ready` endpoint: non-consuming HTTP poll so the client waits before claiming the WebSocket slot
- Get-started guide in `runlease` root command long-help

### Fixed
- `pkg/credential`: host key generated in OpenSSH format (`-----BEGIN OPENSSH PRIVATE KEY-----`) — PKCS#8 format was rejected by sshd
- `pkg/relay/auth.go`: Google ID token auth now accepts personal Gmail accounts (gcloud client ID audience fallback)
- `pkg/provider/gcp`: project ID auto-detected from `GOOGLE_CLOUD_PROJECT`, `GCLOUD_PROJECT`, or `gcloud config get-value project` — env var no longer required
- `cmd/runlease/up.go`: SSH poller retries on "host key mismatch" to handle the race where sshd starts before the startup script injects the host key
- `runlease configure`: Ctrl+C now exits cleanly — goroutine calls `os.Exit(0)` on SIGINT since Go retries EINTR on blocking reads
- `pkg/relay/server.go`: coordinator now loops after a session ends — resets `instanceClaimed`/`instanceReady` so the agent can reconnect and `runlease connect` works on the same instance indefinitely
- `cmd/runlease-agent`: increased "waiting for paired" read deadline from 60 s to 2 h to prevent premature reconnects while idle
- Auto-generated instance names now use prefix `rl-spot-` instead of `{provider}-spot-`
- `.gitignore`: anchor binary patterns with `/` so `cmd/runlease-*` source directories are not excluded

## [0.0.1] — 2026-05-02

### Added

**CLI (`cmd/runlease`)**
- `up` — provisions a spot instance from a manifest YAML; pre-registers relay token; polls until SSH/relay is ready
- `down` — terminates an instance by name or ID
- `ssh` — opens a direct SSH session (no relay)
- `connect` — opens an SSH session tunnelled through the relay (no public IP required)
- `list` — lists running instances with PROVIDER, PUBLIC IP, REGION, AGE, EXPIRES columns; `--label` filter
- `catalog` — lists all instance classes per provider
- `iap` — GCP IAP TCP tunnel SSH (GCP-only)

**Relay server (`cmd/runlease-relay`)**
- WebSocket relay at `/relay`; token pre-registration model (CLI is authority)
- `GET /health`, `GET /agent` (binary download), `GET /ready` (non-consuming instance-ready poll)
- Google ID token auth with email/domain allow-list (`--audience`, `--allowed-emails`, `--allowed-domains`)
- Deployed to GCP e2-micro at `relay.snapmatch.ai` with Caddy + Let's Encrypt TLS

**Relay agent (`cmd/runlease-agent`)**
- Runs on launched instances; connects outbound to relay over WebSocket; bridges `localhost:22` ↔ relay; auto-reconnects

**Packages**
- `pkg/relay` — `Server` (http.Handler), `wsConn` net.Conn adapter, `AuthConfig` Google OAuth allow-list
- `pkg/provider` — `CloudProvider` interface; AWS (spot via `RequestSpotInstances`), GCP (spot via `ProvisioningModel: SPOT`), Azure (stub)
- `pkg/credential` — ephemeral ed25519 keypair generation (OpenSSH format), AES-256-GCM encrypted storage
- `pkg/manifest` — YAML manifest loader/validator; `WantsPublicIP()` helper
- `pkg/instanceclass` — abstract class resolver (`compute.small`, `compute.large`, etc.) per provider
- `pkg/registry` — local JSON registry of running instances
- `pkg/lifecycle` — TTL-based reclaim / expiry tracking
- `pkg/userdata` — bash startup-script templating (SSH key injection, sshd hardening, relay agent install)

**Examples**
- `examples/manifests/example.yaml` — AWS manifest example
- `examples/manifests/gcp-relay.yaml` — GCP + relay manifest example

**Docs**
- `docs/k3s-bastion-integration.md` — integration plan for k3s on spot instances

**Project**
- `.claude/commands/wrap-up.md` — `/wrap-up` session wrap-up slash command
