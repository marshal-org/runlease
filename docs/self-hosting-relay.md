| `--agent-cache-dir` | Where agents of other releases are kept once fetched. Default: `runlease-relay-agents` in the temp directory. Empty serves only `--agent-binary` by digest. |
| `--agent-release-url` | Where an agent of another release is fetched from; `{version}` is replaced by the tag the CLI names. Default: the GitHub release. Empty turns fetching off. |
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
- The `runlease-relay` and `runlease-agent` release binaries. The relay serves every CLI
  release its own agent (see [Agents for every CLI release](#agents-for-every-cli-release)), so
  the CLI your team uses can be newer or older than the relay.

## Get the binaries

```bash
VERSION=v0.7.1
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
| `--google-client-id` | OAuth client IDs whose ID tokens are accepted. `runlease login` signs people in with the first. |
| `--google-client-secret` | Secret of that client, handed to CLIs for the sign-in flow. Also read from `RUNLEASE_GOOGLE_CLIENT_SECRET`. |
| `--tiers-file` | YAML file of tiers and per-identity limits. |
| `--tiers-reload` | How often to re-read the tiers file (default `15s`). `0` reads it only at start and on `SIGHUP`. |
| `--max-slots` | Refuse new slots beyond this many, across everyone. Default 5000, `0` for no cap. |
| `--insecure-no-auth` | Run with no authentication. The relay will not start open without it. |

## Who may open a slot

Opening a slot (`runlease up`) needs a Google ID token for an account on the allow-list. The CLI
gets one from application default credentials or from `gcloud`.

- Set `--audience` **and** at least one of `--allowed-emails` or `--allowed-domains`. With an
  audience and no allow-list, any verified Google account is admitted.
- Only verified email addresses are matched.
- Agents and CLI clients do not present an identity. They present the slot token, which only
  works for a slot that an allowed identity opened.

## Sign-in with `runlease login`

A relay with only `--audience` expects people to have `gcloud` or a service account. To let
anyone with a Google account sign in from the CLI, give the relay an OAuth client:

1. In the Google Cloud console, configure the OAuth consent screen (scopes `openid` and `email`)
   and create an OAuth client of type **TVs and Limited Input devices**.
2. Start the relay with `--google-client-id` and `--google-client-secret`.

The relay publishes both at `/.well-known/runlease-relay`, and `runlease login` reads them from
there. For this client type the secret is not confidential; Google documents it that way for
installed applications.

With a client ID and no allow-list, any verified Google account may sign in. Use tiers to decide
what each of them gets.

## Tiers and limits

A tiers file gives each identity a set of limits:

```yaml
default: free
tiers:
  free:
    maxSlots: 2      # machines one person may have connected at once
    slotTTL: 8h      # a slot lives this long at most
    idleTTL: 15m     # a slot whose machine has been gone this long is given back
  team:
    maxSlots: 20
assign:
  emails:
    someone@example.com: team
  domains:
    example.com: team
```

- Everyone lands in `default` unless an assignment names another tier. An exact email wins over
  a domain.
- A slot is given back when its owner runs `runlease down`, when its machine's agent has been
  absent for `idleTTL`, or when `slotTTL` runs out.
- `runlease whoami` shows a person their tier and usage. `GET /me` is the same thing for scripts.
- A change to the file is applied while the relay runs, within `--tiers-reload` (15 seconds by
  default) or at once on `SIGHUP`. Nobody is disconnected: limits are checked when a machine
  registers, so connected sessions keep the terms they registered with.
- A file that is not valid is not applied. The relay logs why and keeps the limits it has.
  Replace the file by moving a new one into place, so the relay never reads half of it.
- An unknown field in the file is an error, so a misspelt limit cannot silently mean "unlimited".

A full example is in [`deploy/relay/tiers.example.yaml`](../deploy/relay/tiers.example.yaml).

## Deploy with Terraform

[`deploy/relay/gcp`](../deploy/relay/gcp) stands up everything on this page on one Google Cloud
VM: network, firewall, static address, verified binaries, the systemd unit and TLS.

## Usage records

The relay logs three events with the account's email: a machine registered (with the tier and the
CLI version), a slot released (with how long it lived), and a registration refused at a tier
limit. It records nothing about what a machine does, because it cannot see it.

The Terraform module can keep these in BigQuery for you with its `usage` module, and ships
queries for the usual questions. See [the module's README](../deploy/relay/gcp/README.md#usage-records).
Anywhere else, they are ordinary lines on the relay's standard error.

## Point the CLI at it

```bash
runlease config set relay.addr wss://relay.example.com
```

or per manifest:

```yaml
relay:
  addr: wss://relay.example.com
```

## Agents for every CLI release

Each CLI release carries the SHA-256 of the agent built with it, and a new machine runs only that
agent. The CLI asks the relay for it by digest:

```
GET /agent/sha256/<sha256>?version=<the CLI's release tag>
```

The relay serves it from `--agent-binary` or its cache. If it has neither, it downloads that
release's agent from `--agent-release-url`, keeps it only if the SHA-256 is the one asked for, and
serves it from then on. A relay never has to be upgraded for a new CLI release, and an upgraded
relay does not break older CLIs. The machine checks the digest again at boot, so a relay cannot
hand it any other binary.

With fetching off (`--agent-release-url ""`), put the agents you want to serve in
`--agent-cache-dir`, each named by its SHA-256.

CLI, relay and agent also agree on a wire protocol. The relay accepts a range of versions and
refuses one outside it with a message that says to upgrade runlease. `runlease doctor` shows
whether the relay can serve this CLI's agent.

## Upgrading

Upgrade the relay only when it needs a change. CLI releases do not need one.

1. Install the new `runlease-relay` and `runlease-agent`.
2. `systemctl restart runlease-relay`.

Restarting the relay drops live sessions and forgets its slots, because slots live in memory.
VMs that are already running keep their old agent.

## Operating it

- **Logs** go to stderr as structured text. Slot tokens are logged as a short prefix only.
- **Health:** `GET /health` returns 200.
- **State:** all in memory. There is nothing to back up.
- **Network:** the relay needs inbound HTTPS and nothing outbound.
- **Hardening:** keep it on its own small machine with no cloud credentials attached. A relay with
  no credentials is not worth much to an attacker, which is the point of the design.
