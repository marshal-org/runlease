# Manifest reference

A runlease manifest is a YAML file that describes the spot instance you want to launch and what to do once it's up. Authoritative schema: [`pkg/manifest/manifest.go`](../pkg/manifest/manifest.go).

The fastest way to get one is `runlease manifest new` (an interactive wizard). This page documents every field so you can hand-edit one or know what to uncomment in the wizard output.

---

## Minimal manifest

```yaml
name: my-runner
provider: gcp
region: us-central1-a
instanceClass: compute.small
reclaim:
  ttl: 2h
relay:
  addr: wss://relay.example.com
```

Launch with `runlease up -m my-runner-manifest.yaml`.

---

## Top-level fields

### Required

| Field | Type | Notes |
|---|---|---|
| `name` | string | Instance name. Becomes the hostname prefix (`rl-<name>-<rand>`), the registry key, and the `runlease-managed` tag. |
| `provider` | enum | `aws` or `gcp`. |
| `region` | string | AWS region (`us-east-1`) or GCP zone (`us-central1-a`). A GCP region (`europe-west4`) also works: its first zone is used. So does an AWS availability zone (`us-east-1a`): the launch goes to the subnet in that zone. |
| **one of** `instanceClass` **or** `instanceType` | string | Portable class (`compute.small` …) **or** concrete cloud type (`t3.large`, `e2-standard-4`). Mutually exclusive. |

### Image

| Field | Default | Notes |
|---|---|---|
| `baseImage` | auto-detected for `aws` (latest AL2023) and `gcp` (Debian 12 family) | AMI ID for AWS; image family path for GCP. Required for other providers. |
| `diskSizeGb` | image default (GCP Debian 12 ≈ 10 GB; AWS AL2023 ≈ 30 GB) | Boot disk size in GB. GCP sets `DiskSizeGb` on the boot disk; AWS resizes the AMI's root volume (root device looked up via `DescribeImages`). Must be ≥ the image's minimum — clouds reject shrinking. |

### Networking

| Field | Type | Default | Notes |
|---|---|---|---|
| `publicIp` | bool | `false` with a relay, `true` without | Attach a public IP. With a relay the VM needs none: it dials out through your subnet's NAT gateway, and its SSH server listens on localhost only. If the subnet has no NAT gateway, set `true` (or pass `--public-ip`); the address then carries outbound traffic only. `runlease up` checks the subnet for a NAT gateway first (Cloud NAT on GCP, the route table on AWS) and refuses to launch a machine that could not reach the relay. |
| `relay.addr` | string | — | `wss://…` or `host:port`. Enables the relay tunnel. With this set, no inbound firewall rule is needed. |
| `relay.agentSha256` | string | digest built into release CLIs | SHA-256 the VM checks the relay-served agent against before running it. Set it when your relay serves an agent you built yourself. `unverified` skips the check on purpose. See [agent verification](security.md#the-agent-binary-is-verified-before-it-runs). |

### Lifecycle

| Field | Type | Default | Notes |
|---|---|---|---|
| `reclaim.ttl` | duration | — | `2h`, `90m`, `1d`. Auto-terminate after this much elapsed time. |
| `reclaim.selfTermination` | bool | `true` | Arm the lease timer inside the VM, so it deletes itself at the end of `ttl`. The deadline is stored on the VM's disk and re-armed at every boot, so a reboot neither loses nor restarts the lease. Set `false` only if something else stops the VM: then nothing on the VM enforces the lease, and `runlease up` says so. On GCP this attaches the default service account with the Compute scope unless `gcp.serviceAccount` is set. |
| `tags` | map[string]string | — | AWS tags / GCP labels. User keys override the auto-injected `runlease-managed`, `runlease-created-by`, `runlease-expires-at`. |

### Sudo

| Field | Type | Default | Notes |
|---|---|---|---|
| `sudo` | enum | `locked` | `locked`: only `/usr/local/bin/runlease-terminate` is `NOPASSWD`. `passwordless`: writes `runlease-agent ALL=(ALL) NOPASSWD: ALL` to `/etc/sudoers.d/runlease-agent-full` at boot. **Cannot be added to a running VM** — set it before launch. |

```yaml
sudo: passwordless
```

### Setup scripts

`setup` runs as root after the runlease bootstrap completes. Output streams to `/var/log/runlease-setup.log`; `/run/runlease-setup-done` is touched on success.

```yaml
setup:
  - name: install-k3s
    run: |
      curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--write-kubeconfig-mode=644" sh -
      until k3s kubectl get nodes 2>/dev/null | grep -q ' Ready'; do sleep 1; done
  - name: seed
    file: ./scripts/seed.sh    # local file inlined at manifest-load time
```

Each step has:
- `name`: optional, used for the step's script filename `/var/lib/runlease/setup/NN-name.sh` and the section header in the log.
- `run`: inline bash. Mutually exclusive with `file`.
- `file`: local file path (relative to the manifest dir). Inlined into the userdata at launch — the VM doesn't need to fetch it.

Idempotency is your problem. The wrapping script uses `set -euo pipefail`, so a failing step halts and the `__RUNLEASE_SETUP_DONE__` marker is never written (which is the signal `runlease up --attach` watches for).

### Forwards

`forwards` is the declarative version of `runlease forward -L`. Applied by `runlease up --attach` and by `runlease forward --all <name>`.

```yaml
forwards:
  - name: kube-api
    local: 16443      # listen on this port (default bind 127.0.0.1)
    remote: 6443      # dial this on the instance side
  - name: app
    direction: local  # default; "remote" for ssh -R semantics
    bind: 0.0.0.0     # default 127.0.0.1
    host: localhost   # default; remote-side hostname
    local: 8080
    remote: 80
```

Persisted to the registry at `up` time so `forward --all` doesn't need to re-read the manifest.

### Provider-specific

#### AWS

| Field | Type | Notes |
|---|---|---|
| `aws.securityGroupIDs` | []string | Attach these SGs. |
| `aws.subnetID` | string | Subnet placement. Omit to use the default VPC. In a region with no default VPC, runlease picks a subnet of the one VPC that has a route to the internet (an internet gateway for `publicIp: true`, a NAT gateway otherwise); with several such VPCs it stops and lists the subnets. |
| `aws.instanceProfile` | string | IAM instance profile ARN — required if your setup scripts hit AWS APIs. |

#### GCP

| Field | Type | Notes |
|---|---|---|
| `gcp.serviceAccount` | string | SA email to attach. Needed for `selfTermination: true` (the script uses the metadata server to issue the delete call). The SA must hold the `compute.instances.delete` **IAM permission** — the `cloud-platform` scope runlease grants is necessary but not sufficient; without the IAM grant the self-delete returns 403 and the VM merely powers off, lingering in a `TERMINATED` state. See note below. |
| `gcp.networkTags` | []string | GCE network tags — typically firewall selectors. |
| `gcp.network` | string | VPC network name or self-link. Omit to use the `default` network, or, when the project has none, its only network. |
| `gcp.subnetwork` | string | Subnetwork name or self-link. Omit when the network has exactly one subnetwork in the region: runlease finds and uses it. Setting the subnetwork is enough; GCP infers the network from it, so `gcp.network` is usually optional. |

> **Custom-mode VPCs.** With one subnetwork in the region, nothing needs setting. With several, runlease does not guess: before anything is created, the launch stops and lists each one as the flags to pass (`--subnet SUBNET`, or `gcp.subnetwork` here). `runlease doctor --provider gcp` shows the same choice without launching.

> **Self-termination needs a delete grant — scope it tightly.** When you attach a custom `serviceAccount`, that SA — not the project default — is the principal the self-delete API call runs as. Grant it `compute.instances.delete`, but constrain the grant to runlease-managed VMs rather than handing it project-wide instance-admin. IAM can't express "delete only itself" (the SA isn't bound to one VM), but a name-prefix condition on the `rl-` convention gets close:
>
> ```bash
> gcloud iam roles create runleaseSelfDelete --project PROJECT \
>   --permissions compute.instances.delete
>
> gcloud projects add-iam-policy-binding PROJECT \
>   --member "serviceAccount:SA_EMAIL" \
>   --role "projects/PROJECT/roles/runleaseSelfDelete" \
>   --condition='title=runlease-vms-only,expression=resource.name.matches("projects/PROJECT/zones/[^/]+/instances/rl-.*")'
> ```
>
> For a grant that can't be defeated by name collisions, attach a GCP secure tag at create time and condition on `resource.matchTag(...)` instead.

---

## Full example

```yaml
name: k3s-dev
provider: gcp
region: us-central1-a
instanceClass: compute.large
baseImage: projects/debian-cloud/global/images/family/debian-12

publicIp: false                      # relay-only access
relay:
  addr: wss://relay.example.com

reclaim:
  ttl: 4h
  selfTermination: true

tags:
  env: dev
  team: platform

sudo: passwordless

setup:
  - name: install-k3s
    run: |
      curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--write-kubeconfig-mode=644" sh -
      until k3s kubectl get nodes 2>/dev/null | grep -q ' Ready'; do sleep 1; done

forwards:
  - name: kube-api
    local: 16443
    remote: 6443

gcp:
  serviceAccount: deployer@my-project.iam.gserviceaccount.com
  networkTags:
    - allow-egress-only
```

Launch with `runlease up --attach -m k3s-dev.yaml`.

---

## See also

- [`runlease manifest new`](../cmd/runlease/manifest.go) — interactive wizard
- [`examples/manifests/`](../examples/manifests/) — minimal sample manifests
