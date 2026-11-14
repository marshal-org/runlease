# Self-hosting the relay

The relay is one static Go binary. It needs a public TLS endpoint and almost nothing else: it
keeps no data on disk and holds no keys. Running your own is the recommended setup for real work,
because it keeps connection metadata inside your organisation.

For what the relay can and cannot see, read the [threat model](security.md).

---

## What you need

- A small Linux VM or container. One vCPU and 1 GB of memory is enough to start.
- A DNS name and TLS. Put the relay behind a load balancer, Caddy, nginx or Cloudflare; the relay
  itself speaks plain HTTP and WebSocket on one port.
- The `runlease-relay` and `runlease-agent` binaries **from the same release** as the CLI your
  team uses.

## Get the binaries

```bash
VERSION=v0.5.0
BASE="https://github.com/marshal-org/runlease/releases/download/${VERSION}"
curl -fsSLO "${BASE}/runlease-relay-linux-amd64"
curl -fsSLO "${BASE}/runlease-agent-linux-amd64"
curl -fsSLO "${BASE}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
sudo install -m 0755 runlease-relay-linux-amd64 /usr/local/bin/runlease-relay
sudo install -m 0755 runlease-agent-linux-amd64 /usr/local/bin/runlease-agent
```

Use the release binaries, not a local build. The CLI checks the agent the relay serves against a
SHA-256 compiled into the CLI at release time. A locally built agent has a different digest and
VMs will refuse it. If you must build your own, pin its digest in each manifest with
`relay.agentSha256`.

## Run it

```ini
# /etc/systemd/system/runlease-relay.service
[Unit]
Description=Runlease relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/runlease-relay \
  --addr 127.0.0.1:7777 \
  --agent-binary /usr/local/bin/runlease-agent \
  --audience https://relay.example.com \
  --allowed-domains example.com \
  --max-slots 5000
Restart=always
DynamicUser=yes
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now runlease-relay
curl -fsS http://127.0.0.1:7777/health && echo ok
```

Then terminate TLS in front of it and forward to `127.0.0.1:7777`. WebSocket upgrades must pass
through, and idle timeouts on the proxy should be long: sessions are held open.

## Flags

| Flag | Meaning |
|---|---|
| `--addr` | Listen address. Default `:7777`. |
| `--agent-binary` | Path of the agent served at `GET /agent`. |
| `--audience` | The relay's public URL. Sets the audience the identity token must carry. |
| `--allowed-emails` | Comma-separated accounts that may open slots. |
| `--allowed-domains` | Comma-separated email domains that may open slots. |
| `--max-slots` | Refuse new slots beyond this many. Default 5000, `0` for no cap. |
| `--insecure-no-auth` | Run with no authentication. The relay will not start open without it. |

## Who may open a slot

Opening a slot (`runlease up`) needs a Google ID token for an account on the allow-list. The CLI
gets one from application default credentials or from `gcloud`.

- Set `--audience` **and** at least one of `--allowed-emails` or `--allowed-domains`. With an
  audience and no allow-list, any verified Google account is admitted.
- Only verified email addresses are matched.
- Agents and CLI clients do not present an identity. They present the slot token, which only
  works for a slot that an allowed identity opened.

## Point the CLI at it

```bash
runlease config set relay.addr wss://relay.example.com
```

or per manifest:

```yaml
relay:
  addr: wss://relay.example.com
```

## Upgrading

The relay serves the agent, so **relay and agent are always upgraded together**, and they should
match the CLI release your team runs.

1. Install the new `runlease-relay` and `runlease-agent`.
2. `systemctl restart runlease-relay`.
3. Roll the new CLI out.

Restarting the relay drops live sessions and forgets its slots, because slots live in memory.
VMs that are already running keep their old agent.

## Operating it

- **Logs** go to stderr as structured text. Slot tokens are logged as a short prefix only.
- **Health:** `GET /health` returns 200.
- **State:** all in memory. There is nothing to back up.
- **Network:** the relay needs inbound HTTPS and nothing outbound.
- **Hardening:** keep it on its own small machine with no cloud credentials attached. A relay with
  no credentials is not worth much to an attacker, which is the point of the design.
