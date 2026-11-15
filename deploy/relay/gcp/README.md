# Relay on Google Cloud

A Terraform module that runs a Runlease relay on one small VM: its own network, inbound 80 and
443 only, a static address, a service account with no cloud access, and
[Caddy](https://caddyserver.com) in front for an automatically issued TLS certificate.

The VM downloads the relay and agent binaries, checks their SHA-256 and refuses to install on a
mismatch. The relay keeps nothing on disk.

Read [self-hosting the relay](../../../docs/self-hosting-relay.md) for what the settings mean and
[the threat model](../../../docs/security.md) for what a relay can and cannot see.

## Before you start

1. **A project that holds nothing else.** The relay needs no access to anything; give it nothing
   to reach.
2. **A hostname** you can point at the relay, for example `relay.example.com`.
3. **A Google OAuth client**, if people will sign in with `runlease login`:
   - Google Cloud console, *APIs & Services*, *OAuth consent screen*: user type External, app
     name, support email, your site and privacy policy links. Scopes: `openid` and `email` only.
     Publish the app, otherwise only listed test users can sign in.
   - *Credentials*, *Create credentials*, *OAuth client ID*, application type
     **TVs and Limited Input devices**.
   - Keep the client ID and the client secret. For this client type the secret is not
     confidential: the relay hands it to every CLI that signs in.
4. **The binaries.** Take `runlease-relay-linux-amd64` and `runlease-agent-linux-amd64` from one
   release, with their lines from `checksums.txt`.

## Use it

```hcl
module "relay" {
  source = "github.com/marshal-org/runlease//deploy/relay/gcp?ref=v0.5.0"

  project_id = "my-relay-project"
  hostname   = "relay.example.com"
  acme_email = "ops@example.com"

  relay_binary_url = "https://github.com/marshal-org/runlease/releases/download/v0.5.0/runlease-relay-linux-amd64"
  relay_sha256     = "<from checksums.txt>"
  agent_binary_url = "https://github.com/marshal-org/runlease/releases/download/v0.5.0/runlease-agent-linux-amd64"
  agent_sha256     = "<from checksums.txt>"

  google_client_id     = "1234567890-abc.apps.googleusercontent.com"
  google_client_secret = "GOCSPX-..."

  # Anyone with a Google account may sign in; the tiers set what they get.
  tiers_yaml = file("${path.module}/tiers.yaml")
}

output "address" { value = module.relay.address }
```

```bash
terraform init
terraform apply
```

Then point the hostname's A record at the `address` output (or set `dns_managed_zone` and let
the module create it). The certificate is issued on the first request after DNS resolves.

```bash
curl -fsS https://relay.example.com/health && echo ok
curl -fsS https://relay.example.com/.well-known/runlease-relay
```

## A private relay for one organisation

Leave the OAuth client out and admit your own identities by their Google identity tokens:

```hcl
  audience        = "https://relay.example.com"
  allowed_domains = ["example.com"]
```

## Binaries from a private bucket

When the release is not publicly downloadable, upload the two binaries to a bucket in the same
project and give `gs://` URLs:

```hcl
  relay_binary_url = "gs://my-relay-binaries/v0.5.0/runlease-relay-linux-amd64"
  agent_binary_url = "gs://my-relay-binaries/v0.5.0/runlease-agent-linux-amd64"
  binary_bucket    = "my-relay-binaries"
```

## Upgrading

Change the binary URLs and checksums and apply. The startup script changes, so Terraform replaces
the VM. Live sessions drop and slots are forgotten, because slots live in memory. Upgrade the
relay before you roll out the CLI release that goes with it: a release CLI only accepts the agent
from its own release.

## Operating

| | |
|---|---|
| Logs | `gcloud compute ssh` through IAP (`allow_iap_ssh = true`), then `journalctl -u runlease-relay -u caddy`. Serial console output shows the boot script. |
| Health | `GET /health` |
| Change limits | Edit `tiers_yaml` and apply |
| Shut it off | `terraform destroy`. Nothing is stored, so nothing is lost. |

## Inputs

See [`variables.tf`](variables.tf). The ones without a default are `project_id`, `hostname`,
`acme_email`, and the two binary URLs with their checksums.
