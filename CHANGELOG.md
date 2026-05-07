# Changelog

All notable changes to this project will be documented here.

---

## [0.0.5] — 2026-05-08

### Added
- `cmd/runlease/root.go`: `RUNLEASE_HOME` env var — overrides the base directory for all runlease state (`config.yaml`, `registry.json`, `master.key`, `credentials/`); defaults to `~/.runlease` when unset

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
