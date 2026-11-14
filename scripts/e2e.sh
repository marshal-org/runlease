#!/usr/bin/env bash
# End-to-end check against a real cloud account. Run by .github/workflows/e2e.yml.
#
# Launches one small spot VM through the relay, uses it, deletes it, and then
# asks the cloud API directly whether it is really gone. Fails if anything this
# run created is left behind.
#
# Required environment:
#   E2E_GCP_PROJECT   project to launch in (a dedicated, empty one)
#   E2E_GCP_ZONE      e.g. us-central1-a
#   E2E_RELAY_ADDR    e.g. wss://relay.example.com
set -euo pipefail

: "${E2E_GCP_PROJECT:?}" "${E2E_GCP_ZONE:?}" "${E2E_RELAY_ADDR:?}"

work="$(mktemp -d)"
export RUNLEASE_HOME="$work/home"
export GOOGLE_CLOUD_PROJECT="$E2E_GCP_PROJECT"
name="e2e-$(date +%s)"
bin="$work/runlease"

go build -o "$bin" ./cmd/runlease

list_leftovers() {
  gcloud compute instances list --project "$E2E_GCP_PROJECT" \
    --filter="labels.e2e-run=$name" --format="value(name,zone)"
}

cleanup() {
  status=$?
  "$bin" down "$name" --yes >/dev/null 2>&1 || true
  # The local registry is not the source of truth. Ask the cloud.
  left="$(list_leftovers || true)"
  if [ -n "$left" ]; then
    echo "LEAKED: $left" >&2
    echo "$left" | while read -r vm zone; do
      gcloud compute instances delete "$vm" --zone "$zone" --project "$E2E_GCP_PROJECT" --quiet || true
    done
    status=1
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

{
  echo "name: $name"
  echo "provider: gcp"
  echo "region: $E2E_GCP_ZONE"
  echo "instanceClass: compute.small"
  echo "reclaim:"
  echo "  ttl: 20m"
  echo "  selfTermination: true"
  echo "relay:"
  echo "  addr: $E2E_RELAY_ADDR"
  echo "tags:"
  echo "  e2e-run: $name"
} > "$work/manifest.yaml"

echo "== up"
"$bin" up -m "$work/manifest.yaml" --detach

echo "== exec"
out=""
for _ in $(seq 1 30); do
  if out="$("$bin" exec "$name" -- echo runlease-e2e-ok 2>/dev/null)"; then break; fi
  sleep 10
done
if [ "$out" != "runlease-e2e-ok" ]; then
  echo "exec did not return the expected output: ${out:-<nothing>}" >&2
  exit 1
fi

echo "== push and pull round trip"
head -c 65536 /dev/urandom > "$work/blob"
"$bin" push "$name" "$work/blob" /tmp/blob
"$bin" pull "$name" /tmp/blob "$work/blob.back"
cmp "$work/blob" "$work/blob.back"

echo "== the login user must not have general sudo"
if "$bin" exec "$name" -- sudo -n true 2>/dev/null; then
  echo "sudo is open by default" >&2
  exit 1
fi

echo "== the agent must not run as root"
owner="$("$bin" exec "$name" -- sh -c 'ps -o user= -C runlease-agent | head -1' | tr -d ' ')"
if [ "$owner" != "runlease-agent" ]; then
  echo "agent runs as: $owner" >&2
  exit 1
fi

echo "== down"
"$bin" down "$name" --yes

echo "== confirm with the cloud that it is gone"
for _ in $(seq 1 18); do
  left="$(list_leftovers)"
  if [ -z "$left" ]; then
    echo "PASS"
    exit 0
  fi
  sleep 10
done
echo "instance still exists after down: $left" >&2
exit 1
