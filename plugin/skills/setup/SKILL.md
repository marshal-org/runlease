---
name: setup
description: Install Runlease and get to a first working machine. Use when `runlease` is not installed, the person asks to set Runlease up, or a Runlease command fails because of a missing sign-in, project, region or relay.
---

# Set Runlease up

Runlease launches a spot machine in the person's **own** Google Cloud or AWS account, reaches it
through a relay, and deletes it when its lease ends. Setup has three parts: the CLI, a cloud
sign-in, and a relay sign-in. Check each before fixing anything.

## 1. Find out what is missing

```bash
runlease version          # not found -> install (step 2)
runlease doctor           # checks sign-ins, project/account, network and relay, and prints each fix
```

`runlease doctor` creates nothing and is safe to run at any time. Act on its `fix:` lines, in
order. If this build has no `doctor` command, check by hand with `runlease whoami` and step 3.

## 2. Install

```bash
curl -fsSL https://runlease.online/install.sh | sh     # Linux and macOS, to ~/.local/bin, no sudo
```

The installer verifies the binary's SHA-256. Do not use `go install`: a source build cannot verify
the agent that runs on each machine.

## 3. Sign-ins: the person does these, not you

Each of these opens a browser or shows a code. Ask the person to run it in their own terminal (in
Claude Code they can type `! <command>` at the prompt), then continue.

| What | Command | When |
|---|---|---|
| Google Cloud | `gcloud auth application-default login` then `gcloud config set project PROJECT` | Launching on GCP |
| AWS | `aws sso login` (or `aws login`) | Launching on AWS |
| Relay | `runlease login` | Always. With no relay chosen it uses the free public relay and saves it |

`gcloud auth login` is **not** enough: Runlease reads the *application default* sign-in.

## 4. First machine

```bash
runlease up -p gcp -n box -d        # or -p aws. Prints the project/account it will use: read it
runlease exec box -- uname -a
runlease down box --yes
```

Always read the `☁` line `up` prints. It names the project or account, the region and the identity.
If it is not the one the person expects, stop and ask before launching.

## Rules

- Never pass `--public-ip` on your own. If `up` says the subnet has no NAT gateway, show the person
  the choices it lists and let them pick.
- Never run a sign-in command in the background or try to complete it yourself.
- `runlease configure` is interactive. To save defaults without it:
  `runlease config set defaults.provider gcp`, `defaults.region`, `defaults.size`, `defaults.ttl`.
