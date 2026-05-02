# k3s / Bastion Integration Plan

## Context

Runlease provisions ephemeral spot instances with a secure relay tunnel. This document describes how to extend Runlease so that an AI agent (running in a constrained pod) can use a spot VM as a fully capable build-and-deploy target — specifically for the Bastion platform during development.

The agent stays in the pod. The spot instance runs k3s. The agent builds code and pushes images from the pod, deploys to k3s over a relay-tunneled kubectl channel, and observes results. When the session ends the cluster is gone.

---

## Architecture

```
┌─────────────────────┐         ┌──────────────────────────────────┐
│   Agent Pod         │         │   Spot Instance (Runlease)       │
│                     │ relay   │                                  │
│  build / go test    │◄───────►│  k3s (cluster)                   │
│  docker build       │ (SSH)   │  └─ bastion-dev namespace        │
│  docker push ──────────────────────────► registry (external)     │
│  kubectl ──────────►│         │  ◄── k3s pulls from registry     │
│    (via relay proxy)│         │                                  │
└─────────────────────┘         └──────────────────────────────────┘
                │
        relay.runlease.io:7777
```

Key properties:
- The spot cluster has no public IP, no inbound firewall rules
- The only access path is through the relay token issued at launch
- The agent has no cloud credentials beyond what the pod's service account grants
- The cluster dies with the instance TTL — no cleanup required

---

## What Changes in Runlease

### 1. Manifest — new `k3s` block

```yaml
k3s:
  enabled: true
  version: "v1.32.0+k3s1"   # optional; defaults to latest stable
  namespace: bastion-dev     # created post-boot
  serviceAccount: agent      # namespace-scoped SA; kubeconfig uses its token
```

**File:** `pkg/manifest/manifest.go`
```go
type K3sConfig struct {
    Enabled        bool   `yaml:"enabled"`
    Version        string `yaml:"version"`
    Namespace      string `yaml:"namespace"`
    ServiceAccount string `yaml:"serviceAccount"`
}
```

### 2. Userdata — k3s install block

When `k3s.enabled` is true, `userdata.Build()` appends to the startup script:

```bash
# Install k3s
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=<version> sh -
# k3s starts as systemd service and writes /etc/rancher/k3s/k3s.yaml
```

If `namespace` and `serviceAccount` are set, the script also creates them and applies RBAC (namespace-scoped: deployments, pods, services, configmaps, secrets in that namespace only):

```bash
until k3s kubectl get nodes --no-headers 2>/dev/null | grep -q Ready; do sleep 3; done
k3s kubectl create namespace <namespace>
k3s kubectl create serviceaccount <sa> -n <namespace>
# apply ClusterRole + RoleBinding
```

**File:** `pkg/userdata/userdata.go`

### 3. `runlease up` — post-SSH bootstrap step

After the SSH session is ready, if `k3s.enabled`:

1. Poll `k3s kubectl get nodes` via SSH exec until node is Ready (~30–60s)
2. Fetch kubeconfig: `cat /etc/rancher/k3s/k3s.yaml` over SSH exec
3. If `serviceAccount` is set, fetch its token instead of the admin cert:
   `k3s kubectl create token <sa> -n <namespace> --duration=<ttl>`
4. Rewrite `server: https://127.0.0.1:6443` → `server: https://localhost:<localPort>`
5. Write to `~/.runlease/kubeconfigs/<name>.yaml` (mode 0600)
6. Store path in registry entry
7. Start SSH port-forward goroutine: `localhost:<localPort>` → `spot:6443`

**File:** `cmd/runlease/up.go`

Port-forward implementation uses `golang.org/x/crypto/ssh` (already a dependency):
```go
// client.Dial opens a channel through the existing SSH tunnel
remote, _ := sshClient.Dial("tcp", "localhost:6443")
go io.Copy(localConn, remote)
go io.Copy(remote, localConn)
```

### 4. Registry — `KubeconfigPath` field

```go
type Entry struct {
    Handle          provider.InstanceHandle `json:"handle"`
    CredDir         string                  `json:"credDir"`
    RelayAddr       string                  `json:"relayAddr,omitempty"`
    RelayToken      string                  `json:"relayToken,omitempty"`
    KubeconfigPath  string                  `json:"kubeconfigPath,omitempty"`  // new
}
```

### 5. New command: `runlease proxy <name>`

Re-establishes the kubectl port-forward without opening an interactive shell. Useful when the session was detached (`--detach`) or reconnecting.

```
runlease proxy my-runner
# Forwarding localhost:16443 → my-runner:6443 via relay. Press Ctrl+C to stop.
```

**File:** `cmd/runlease/proxy.go`

### 6. New command: `runlease kubeconfig <name>`

Prints the path to the kubeconfig file for use in scripts or IDE integrations.

```
runlease kubeconfig my-runner
# /Users/you/.runlease/kubeconfigs/my-runner.yaml
```

---

## Security Properties

| Concern | How it's handled |
|---|---|
| Cluster access requires relay token | k3s API never reachable without the SSH tunnel; spot instance has no public IP |
| Agent can't reach other sessions | Each relay token is unique per instance; no cross-session routing |
| Agent can't escalate k8s privileges | Kubeconfig uses SA token scoped to one namespace; cluster-admin cert stays on the VM |
| Credentials expire with the session | SA token TTL matches instance TTL; kubeconfig is useless after relay token is gone |
| Compromised spot instance | Agent's pod has no cloud credentials; relay token only routes to that instance; k3s API requires the kubeconfig client cert |

---

## Example Manifest

```yaml
name: bastion-dev
provider: aws
region: us-east-1
instanceClass: compute.large
baseImage: ami-0abcdef1234567890   # Ubuntu 24.04 LTS
reclaim:
  ttl: 4h
relay:
  addr: relay.example.com:7777
k3s:
  enabled: true
  namespace: bastion-dev
  serviceAccount: agent
tags:
  purpose: dev-cluster
  owner: agent
```

---

## Implementation Order

1. `K3sConfig` in manifest + validation
2. k3s install block in userdata (with tests)
3. `KubeconfigPath` in registry
4. Post-SSH bootstrap in `up.go` (poll, fetch, rewrite, port-forward)
5. `runlease proxy` command
6. `runlease kubeconfig` command
7. End-to-end test with a real AWS instance
