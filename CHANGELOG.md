# Changelog

All notable changes to this project will be documented here.

---

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
