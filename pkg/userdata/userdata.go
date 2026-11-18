package userdata

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"
)

const script = `#!/bin/bash
set -euo pipefail

# ── once only ────────────────────────────────────────────────────────────────
# GCP runs this script at every boot, AWS only at the first. Everything below
# is first-boot setup. On a later boot the lease is re-armed by
# runlease-lease.service from the deadline on disk, not from here: running this
# again would start the lease over.
mkdir -p /var/lib/runlease
if [ -e /var/lib/runlease/booted ]; then
  exit 0
fi
touch /var/lib/runlease/booted

# ── failure signal ───────────────────────────────────────────────────────────
# If this script stops early, say so on the serial console, with the stage it
# was in. Nothing else can: the agent may not be up, so the CLI would only see
# a machine that never connects. 'runlease up' reads the console for this line.
RUNLEASE_STAGE=start
trap 'rc=$?; if [ "$rc" -ne 0 ]; then echo "RUNLEASE_BOOT_FAILED stage=$RUNLEASE_STAGE exit=$rc" | tee /dev/console >&2 || true; fi' EXIT

# ── terminate script ─────────────────────────────────────────────────────────
# Always installed — it's a small convenience tool, not a policy. The
# reclaim.selfTermination field only controls whether the systemd-run timer
# below auto-fires it at TTL.
{{- if eq .Provider "gcp" }}
cat > /usr/local/bin/runlease-terminate << 'TERM_EOF'
#!/bin/bash
[ "$EUID" -ne 0 ] && exec sudo "$0" "$@"
systemctl stop runlease-agent 2>/dev/null || true
TOKEN=$(curl -sf -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
  | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
PROJECT=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/project/project-id)
ZONE=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/zone | cut -d/ -f4)
NAME=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/name)
curl -sf -X DELETE \
  -H "Authorization: Bearer $TOKEN" \
  "https://compute.googleapis.com/compute/v1/projects/$PROJECT/zones/$ZONE/instances/$NAME" \
  || systemctl --force --force poweroff
TERM_EOF
{{- else if eq .Provider "aws" }}
cat > /usr/local/bin/runlease-terminate << 'TERM_EOF'
#!/bin/bash
[ "$EUID" -ne 0 ] && exec sudo "$0" "$@"
systemctl stop runlease-agent 2>/dev/null || true
systemctl --force --force poweroff
TERM_EOF
{{- end }}
chmod +x /usr/local/bin/runlease-terminate

RUNLEASE_STAGE=lease
# ── lease timer ──────────────────────────────────────────────────────────────
# The lease is a deadline stored on disk (epoch seconds). runlease-lease-arm
# turns it into a systemd timer, and runlease-lease.service runs that at every
# boot. A transient timer alone would vanish on reboot and the machine would
# then run, and bill, with no end.
cat > /usr/local/bin/runlease-lease-arm << 'ARM_EOF'
#!/bin/bash
[ "$EUID" -ne 0 ] && exec sudo "$0" "$@"
DEADLINE=$(cat /var/lib/runlease/lease-deadline 2>/dev/null || true)
case "$DEADLINE" in ''|*[!0-9]*) exit 0 ;; esac
LEFT=$(( DEADLINE - $(date +%s) ))
[ "$LEFT" -lt 1 ] && LEFT=1
systemctl stop runlease-selfterminate.timer 2>/dev/null || true
systemctl reset-failed runlease-selfterminate.timer runlease-selfterminate.service 2>/dev/null || true
systemd-run --unit=runlease-selfterminate --on-active="$LEFT" /usr/local/bin/runlease-terminate
ARM_EOF
chmod +x /usr/local/bin/runlease-lease-arm

cat > /etc/systemd/system/runlease-lease.service << 'LEASE_EOF'
[Unit]
Description=Runlease lease timer (re-armed from the stored deadline at boot)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/runlease-lease-arm

[Install]
WantedBy=multi-user.target
LEASE_EOF
systemctl daemon-reload
systemctl enable runlease-lease.service

# ── snooze script ────────────────────────────────────────────────────────────
# Moves the deadline to its argument (seconds) from now and re-arms the timer.
# Used by 'runlease snooze' over the relay.
cat > /usr/local/bin/runlease-snooze << 'SNOOZE_EOF'
#!/bin/bash
[ "$EUID" -ne 0 ] && exec sudo "$0" "$@"
SECS="$1"
case "$SECS" in ''|*[!0-9]*) echo "usage: runlease-snooze <seconds-from-now>" >&2; exit 1 ;; esac
# Written to a temp file, flushed and renamed, so a crash or hard reset right
# after a snooze cannot leave an empty deadline (which would mean no lease).
echo $(( $(date +%s) + SECS )) > /var/lib/runlease/lease-deadline.new
sync /var/lib/runlease/lease-deadline.new 2>/dev/null || sync
mv -f /var/lib/runlease/lease-deadline.new /var/lib/runlease/lease-deadline
exec /usr/local/bin/runlease-lease-arm
SNOOZE_EOF
chmod +x /usr/local/bin/runlease-snooze

cat > /etc/sudoers.d/runlease-terminate << 'SUDO_EOF'
runlease-agent ALL=(ALL) NOPASSWD: /usr/local/bin/runlease-terminate
runlease-agent ALL=(ALL) NOPASSWD: /usr/local/bin/runlease-snooze
SUDO_EOF
chmod 440 /etc/sudoers.d/runlease-terminate
{{- if and .SelfTermination .TTLSeconds }}
# Armed here, before anything below that can fail.
echo $(( $(date +%s) + {{ .TTLSeconds }} )) > /var/lib/runlease/lease-deadline
sync /var/lib/runlease/lease-deadline 2>/dev/null || sync
/usr/local/bin/runlease-lease-arm
{{- end }}

RUNLEASE_STAGE=user
# ── runlease-agent user ──────────────────────────────────────────────────────
useradd -m -s /bin/bash runlease-agent
mkdir -p /home/runlease-agent/.ssh
chmod 700 /home/runlease-agent/.ssh

cat > /home/runlease-agent/.ssh/authorized_keys << 'RUNLEASE_EOF'
{{ .PublicKeySSH }}RUNLEASE_EOF
chmod 600 /home/runlease-agent/.ssh/authorized_keys
chown -R runlease-agent:runlease-agent /home/runlease-agent/.ssh

# ── SSH host key (pre-generated by controller) ───────────────────────────────
# Injecting a known host key means the controller can verify identity before
# the instance boots — no trust-on-first-use window.
echo '{{ .HostPrivKeyB64 }}' | base64 -d > /etc/ssh/ssh_host_ed25519_key
chmod 600 /etc/ssh/ssh_host_ed25519_key
echo '{{ .HostPublicKeySSH }}' > /etc/ssh/ssh_host_ed25519_key.pub
chmod 644 /etc/ssh/ssh_host_ed25519_key.pub

# Remove auto-generated host keys so sshd only offers our ed25519 key.
rm -f /etc/ssh/ssh_host_rsa_key* /etc/ssh/ssh_host_ecdsa_key* /etc/ssh/ssh_host_dsa_key*

RUNLEASE_STAGE=sshd
# ── sshd hardening ───────────────────────────────────────────────────────────
# sshd keeps the FIRST value it reads for a keyword, so this block goes at the
# top of sshd_config: ahead of the image's own settings and of any Include of
# sshd_config.d. Appending it would let the image's values win.
cat > /etc/ssh/sshd_config.runlease << 'SSHD_EOF'
# runlease hardening, written at launch. Keep this block first in the file.
PasswordAuthentication no
PermitEmptyPasswords no
PubkeyAuthentication yes
AuthenticationMethods publickey
PermitRootLogin no
AllowUsers runlease-agent
MaxAuthTries 3
LoginGraceTime 20
ClientAliveInterval 120
ClientAliveCountMax 2
{{- if .RelayAddr }}
# Relay mode: the agent on this machine is the only way in, and it dials sshd
# on localhost. Nothing listens on the network.
ListenAddress 127.0.0.1
{{- end }}
SSHD_EOF
{{- if .RelayAddr }}
# ListenAddress is additive, not first-wins, so the image's own lines have to go.
sed -i -E '/^[[:space:]]*ListenAddress[[:space:]]/Id' /etc/ssh/sshd_config
for f in /etc/ssh/sshd_config.d/*.conf; do
  [ -f "$f" ] && sed -i -E '/^[[:space:]]*ListenAddress[[:space:]]/Id' "$f"
done
{{- end }}
echo >> /etc/ssh/sshd_config.runlease
cat /etc/ssh/sshd_config >> /etc/ssh/sshd_config.runlease
# Write through the existing file so its owner, mode and label are kept.
cat /etc/ssh/sshd_config.runlease > /etc/ssh/sshd_config
rm -f /etc/ssh/sshd_config.runlease

# Images that start sshd from a systemd socket (Ubuntu 24.04) take the listen
# address from a unit generated out of sshd_config, so regenerate and restart
# the socket too.
systemctl daemon-reload
if systemctl is-active --quiet ssh.socket 2>/dev/null; then
  systemctl stop ssh.service 2>/dev/null || true
  systemctl restart ssh.socket
else
  systemctl restart sshd 2>/dev/null || systemctl restart ssh 2>/dev/null || service ssh restart 2>/dev/null || true
fi

# Check what sshd will actually enforce. If the hardening did not take, stop
# sshd and stop the bootstrap: the machine stays unreachable and the
# self-termination timer armed above removes it.
mkdir -p /run/sshd
SSHD_EFFECTIVE=$(sshd -T 2>/dev/null || true)
sshd_ok=1
# grep reads a here-string, not a pipe from echo: grep -q exits at the first
# match, and with pipefail an echo still writing would then fail the check.
for want in 'passwordauthentication no' 'permitemptypasswords no' 'permitrootlogin no' \
            'authenticationmethods publickey' 'allowusers runlease-agent'; do
  grep -qix "$want" <<< "$SSHD_EFFECTIVE" || { echo "runlease: sshd setting not in effect: $want" >&2; sshd_ok=0; }
done
{{- if .RelayAddr }}
# No pipe here either: a grep -q that exits early would turn the pipeline's
# status into a failure, and this check would then pass when it should not.
SSHD_LISTEN=$(grep -i '^listenaddress ' <<< "$SSHD_EFFECTIVE" || true)
if [ -z "$SSHD_LISTEN" ] || grep -qvix 'listenaddress 127.0.0.1:22' <<< "$SSHD_LISTEN"; then
  echo 'runlease: sshd would listen beyond localhost' >&2; sshd_ok=0
fi
{{- end }}
if [ "$sshd_ok" -ne 1 ]; then
  systemctl stop ssh.socket ssh.service sshd.service 2>/dev/null || true
  exit 1
fi
{{- if .PasswordlessSudo }}

# ── passwordless sudo (opt-in via manifest.sudo: passwordless) ───────────────
echo "runlease-agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/runlease-agent-full
chmod 440 /etc/sudoers.d/runlease-agent-full
{{- end }}
{{- if .RelayAddr }}

# ── runlease relay agent ──────────────────────────────────────────────────────
RUNLEASE_STAGE=agent-download
# A bounded connect: with no route out this fails in about a minute and a half,
# and the failure line reaches the console while 'runlease up' is still waiting.
curl -fsSL --connect-timeout 20 --retry 3 '{{ .RelayAgentURL }}' -o /usr/local/bin/runlease-agent
{{- if .AgentSHA256 }}
# The agent is fetched from the relay, which is not trusted. Check it against
# the digest the CLI was built with before it is ever executed. On a mismatch
# the binary is removed and bootstrap stops; the self-termination timer armed
# above still fires.
RUNLEASE_STAGE=agent-verify
if ! echo '{{ .AgentSHA256 }}  /usr/local/bin/runlease-agent' | sha256sum -c - >/dev/null 2>&1; then
  rm -f /usr/local/bin/runlease-agent
  echo 'runlease: agent binary from the relay does not match the expected SHA-256, refusing to run it' >&2
  exit 1
fi
{{- end }}
RUNLEASE_STAGE=agent-start
chmod 755 /usr/local/bin/runlease-agent

cat > /etc/runlease-agent.env << 'ENVEOF'
RUNLEASE_RELAY_ADDR={{ .RelayAddr }}
RUNLEASE_RELAY_TOKEN={{ .RelayToken }}
ENVEOF
chmod 600 /etc/runlease-agent.env

cat > /etc/systemd/system/runlease-agent.service << 'SVCEOF'
[Unit]
Description=Runlease relay agent
After=network.target

[Service]
EnvironmentFile=/etc/runlease-agent.env
ExecStart=/usr/local/bin/runlease-agent
Restart=always
RestartSec=5
# The agent only dials out to the relay and to sshd on localhost. It needs no
# root, no capabilities and no write access to the system.
User=runlease-agent
Group=runlease-agent
NoNewPrivileges=true
CapabilityBoundingSet=
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
systemctl enable --now runlease-agent
{{- end }}

# Everything above is now on disk, so a hard reset from here on finds a
# complete machine rather than half-written config.
sync
{{- if .SetupSteps }}

# ── user setup scripts (from manifest.setup) ─────────────────────────────────
# Each step is written to /var/lib/runlease/setup/NN-name.sh and executed with
# its output tee'd to /var/log/runlease-setup.log. The log ends with one of two
# markers, so 'up --attach' always learns how it went: __RUNLEASE_SETUP_DONE__
# (and /run/runlease-setup-done), or __RUNLEASE_SETUP_FAILED__ and the step.
RUNLEASE_STAGE=setup
mkdir -p /var/lib/runlease/setup
: > /var/log/runlease-setup.log
chmod 644 /var/log/runlease-setup.log
{
{{- range $i, $s := .SetupSteps }}
  echo "── runlease setup [{{ printf "%02d" $i }}{{ if $s.Name }} {{ $s.Name }}{{ end }}] ──"
  cat > /var/lib/runlease/setup/{{ printf "%02d" $i }}{{ if $s.Name }}-{{ $s.Name }}{{ end }}.sh << 'RUNLEASE_SETUP_EOF'
{{ $s.Run }}
RUNLEASE_SETUP_EOF
  chmod +x /var/lib/runlease/setup/{{ printf "%02d" $i }}{{ if $s.Name }}-{{ $s.Name }}{{ end }}.sh
  if ! bash /var/lib/runlease/setup/{{ printf "%02d" $i }}{{ if $s.Name }}-{{ $s.Name }}{{ end }}.sh; then
    echo "__RUNLEASE_SETUP_FAILED__ step {{ printf "%02d" $i }}{{ if $s.Name }} ({{ $s.Name }}){{ end }}"
    exit 1
  fi
{{- end }}
  echo "__RUNLEASE_SETUP_DONE__"
  touch /run/runlease-setup-done
} 2>&1 | tee -a /var/log/runlease-setup.log
{{- end }}
`

var tmpl = template.Must(template.New("userdata").Parse(script))

type Params struct {
	PublicKeySSH      string // runlease-agent's authorized public key
	HostPrivateKeyPEM []byte // SSH host private key (base64-encoded before embedding)
	HostPublicKeySSH  string // SSH host public key (authorized_keys format)
	// Optional relay fields; relay setup is skipped when RelayAddr is empty.
	RelayAddr  string
	RelayToken string
	// AgentSHA256 is the expected SHA-256 (64 lowercase hex characters) of the
	// agent binary the relay serves. When set, the boot script verifies the
	// download before running it, so a compromised relay cannot run code on
	// the instance. Empty skips the check.
	AgentSHA256 string
	// AgentURL is where the boot script downloads the agent. Empty means the
	// relay's own agent at <relay>/agent.
	AgentURL string
	// The runlease-terminate script + dedicated sudoers drop-in are always
	// installed (manual escape hatch for the operator). Only the systemd-run
	// auto-fire timer is gated: it requires BOTH SelfTermination=true and
	// TTLSeconds>0.
	Provider        string // "gcp" or "aws"
	SelfTermination bool   // schedule the auto-fire TTL timer
	TTLSeconds      int    // 0 = no timer
	// PasswordlessSudo grants the runlease-agent user NOPASSWD: ALL when true.
	// Default false keeps the locked-down posture; only the runlease-terminate
	// command is sudo-allowed.
	PasswordlessSudo bool
	// SetupSteps are inline scripts to run as root after bootstrap completes.
	// They are executed in order; on success /run/runlease-setup-done is touched.
	SetupSteps []SetupStep
}

// SetupStep is an inline bootstrap script. The userdata template writes each
// to /var/lib/runlease/setup and runs it under bash with output captured to
// /var/log/runlease-setup.log.
type SetupStep struct {
	Name string
	Run  string
}

type templateData struct {
	PublicKeySSH     string
	HostPrivKeyB64   string
	HostPublicKeySSH string
	RelayAddr        string
	RelayToken       string
	RelayAgentURL    string // Params.AgentURL, or http://<RelayAddr>/agent
	AgentSHA256      string
	Provider         string
	SelfTermination  bool
	TTLSeconds       int
	PasswordlessSudo bool
	SetupSteps       []SetupStep
}

// relayHTTPBase converts a relay addr (host:port or ws[s]://...) to its HTTP
// base URL, used for deriving the agent binary download URL.
func relayHTTPBase(addr string) string {
	switch {
	case strings.HasPrefix(addr, "wss://"):
		return "https://" + strings.TrimPrefix(addr, "wss://")
	case strings.HasPrefix(addr, "ws://"):
		return "http://" + strings.TrimPrefix(addr, "ws://")
	default:
		return "http://" + addr
	}
}

func Build(p Params) (string, error) {
	if p.PublicKeySSH == "" {
		return "", fmt.Errorf("PublicKeySSH is required")
	}
	if len(p.HostPrivateKeyPEM) == 0 {
		return "", fmt.Errorf("HostPrivateKeyPEM is required")
	}
	if p.AgentSHA256 != "" && !isSHA256Hex(p.AgentSHA256) {
		return "", fmt.Errorf("AgentSHA256 must be 64 lowercase hex characters")
	}
	agentURL := ""
	if p.RelayAddr != "" {
		agentURL = p.AgentURL
		if agentURL == "" {
			agentURL = relayHTTPBase(p.RelayAddr) + "/agent"
		}
	}
	data := templateData{
		PublicKeySSH:     p.PublicKeySSH,
		HostPrivKeyB64:   base64.StdEncoding.EncodeToString(p.HostPrivateKeyPEM),
		HostPublicKeySSH: strings.TrimSuffix(p.HostPublicKeySSH, "\n"),
		RelayAddr:        p.RelayAddr,
		RelayToken:       p.RelayToken,
		RelayAgentURL:    agentURL,
		AgentSHA256:      p.AgentSHA256,
		Provider:         p.Provider,
		SelfTermination:  p.SelfTermination,
		TTLSeconds:       p.TTLSeconds,
		PasswordlessSudo: p.PasswordlessSudo,
		SetupSteps:       p.SetupSteps,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering user-data: %w", err)
	}
	return buf.String(), nil
}

// isSHA256Hex reports whether s is exactly 64 lowercase hex characters. The
// value is spliced into a shell script, so nothing else is accepted.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
