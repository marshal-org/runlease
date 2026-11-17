---
name: troubleshoot
description: Diagnose a failing Runlease command or an unreachable machine. Use when `runlease up`, `exec`, `connect` or `down` errors, a machine never connects, or the person suspects machines were left running.
---

# When Runlease does not work

Start with the two commands that explain most failures:

```bash
runlease doctor      # sign-ins, project/account, network, relay: one line each, with the fix
runlease list        # what this computer tracks, and machines in the cloud it does not
```

Runlease errors end with a line saying what to do. Read the whole error before acting.

| The error says | Cause | What to do |
|---|---|---|
| `Google Cloud sign-in has expired or is missing` | Application default sign-in | Ask the person to run `gcloud auth application-default login` |
| `GCP project not configured` | No project set | `gcloud config set project PROJECT`, or `export GOOGLE_CLOUD_PROJECT=PROJECT` |
| `AWS sign-in has expired or is missing` | AWS session | Ask the person to run `aws sso login` |
| `may not use REGION. It may use: ...` / `organization blocks this action` | Region not allowed by the AWS organization, or not enabled | Pass one of the regions the error lists: `--region REGION` |
| `has no region` / `has no zone ... Its zones are` | A region or zone that does not exist | Use one the error lists. A Google Cloud region with no zone letter is fine: the first zone is used |
| `does not fit aws` / `does not fit gcp` | A region written for the other cloud | AWS regions look like `us-east-1`, GCP zones like `us-central1-a` |
| `has no NAT gateway` | A machine with no public IP could not reach the relay | Show the person the choices it lists. Do not add `--public-ip` yourself |
| `places a machine could join` / `VPCs a machine could join` | More than one network fits, and Runlease does not guess | Show the person the `--subnet` lines the error lists and ask which. Do not pick one yourself |
| `has no subnetwork in` / `has no VPC` / `no network named` | Nothing to launch on in that region | The error names the regions that have a network, or the one command that creates one. Show it to the person; do not create networks yourself |
| `no relay set` / `run 'runlease login'` | Relay sign-in | Ask the person to run `runlease login` |
| tier limit reached | Too many machines on the relay | `runlease list`, then `runlease down` one. Stopped machines hold a place for 15 minutes |
| `is already tracked` | The name is in use | `runlease down NAME` first, or use another name |
| agent digest mismatch | The CLI and the relay are on different versions | Update the CLI: rerun the installer |
| `stdin is not a terminal` | `connect` needs a real terminal | Use `runlease exec NAME -- COMMAND` |

## A machine launched but never connects

1. Wait. The first connection takes 30-60 seconds, longer on a cold image.
2. `runlease exec NAME -- true` for up to 3 minutes.
3. If `up` reported a boot failure, it names the stage that failed (`RUNLEASE_BOOT_FAILED stage=...`).
   Report that line to the person.
4. Still nothing: the machine cannot reach the relay. Check the network with `runlease doctor`.
   Take the machine down (`runlease down NAME --yes`) so it does not bill until its lease ends.

## Suspected leftovers

```bash
runlease list                # shows untracked machines found in the cloud, marked
runlease sync                # refresh this computer's record from the cloud
runlease down --leaked       # deletes ONLY stopped machines whose lease has passed
```

`down --leaked` never deletes a running machine. For a running machine nobody recognises, show it
to the person and let them decide. Never delete a machine you did not create in this session
without asking.
