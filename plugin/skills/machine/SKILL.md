---
name: machine
description: Launch, use and return a leased cloud machine with Runlease. Use when a task needs a separate machine - a long build or test run, more cores or a GPU, a clean Linux box, a port to preview, or work that should not run on this computer.
---

# Use a leased machine

A Runlease machine is a spot VM in the person's own cloud account. It costs them money from launch
until it is deleted, and it deletes itself when its lease ends. Treat it like a borrowed tool:
take the smallest one for the shortest time, and give it back.

If `runlease` is missing or a command fails on a sign-in, use the `runlease:setup` skill first.
If the `runlease_*` tools are available, prefer them to the shell: same actions, typed arguments.

## The loop

```bash
runlease up -p gcp -n NAME --ttl 1h -d          # launch. -p aws for AWS. Always -d and always a name
runlease exec NAME -- bash -c 'cd /work && make test'    # run. Compound commands need bash -c
runlease push NAME ./local.tar /tmp/local.tar   # copy a file up
runlease pull NAME /tmp/result.json ./result.json        # copy a file back
runlease snooze NAME 30m                        # set the lease to end 30m from now
runlease down NAME --yes                        # delete it now
runlease list                                   # what is running, time left, cost
```

## Rules that prevent the common mistakes

1. **Name every machine** (`-n`), short and specific to the task. You will need the name for every
   later command.
2. **Always pass `-d`.** Without it `up` tries to open an interactive shell.
3. **Read the `☁` line** `up` prints: project or account, region, identity. Wrong one: stop, ask.
4. **`exec` runs one program.** `runlease exec box -- 'a && b'` fails. Use
   `runlease exec box -- bash -c 'a && b'`.
5. **The first `exec` may need a wait.** The machine takes 30-60 seconds to connect after `up -d`.
   Retry `runlease exec NAME -- true` every 10 seconds, for up to 3 minutes, before giving up.
6. **The machine starts empty.** Nothing from this computer is on it: no repo, no credentials, no
   tools beyond the base image (Debian 12 on GCP, Amazon Linux 2023 on AWS). Push what the job
   needs, or clone it there. Never push SSH keys, cloud credentials or `.env` files unless the
   person asks for exactly that.
7. **Pick the lease from the job**, not from habit: 30m for a test run, 2h for a working session.
   Extend with `snooze` when it is not enough. `snooze` sets the time remaining; it does not add.
8. **Give it back.** When the job is done, `runlease down NAME --yes`, and say so. Do not leave
   machines for the lease to clean up.
9. **No NAT gateway?** `up` stops and lists the choices. Show them to the person. Do not add
   `--public-ip` yourself.

## Sizes

`-s compute.small` is the default (about 2 vCPU / 2 GB). Also: `compute.micro`, `compute.medium`,
`compute.large`, `compute.xlarge`, `compute.2xlarge`, `compute.gpu`. `runlease catalog` lists what
each maps to. `up` prints the hourly rate and the cost of the lease: tell the person the figure
before launching anything larger than `compute.large`, and always before `compute.gpu`.

## Reaching a port

```bash
runlease forward NAME -L 3000:localhost:3000    # blocks while the forward is open
```

Run it in the background and stop it when done. The port is reached through the tunnel; nothing is
opened on the machine's network.

## Limits to know

- The free public relay allows two machines at once per person. `runlease whoami` shows the count.
- A spot machine can be taken back by the cloud at any time. Keep results off it: pull them as you go.
- `runlease list` is a local cache. `runlease sync` refreshes it from the cloud.
