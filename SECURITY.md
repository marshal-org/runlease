# Security policy

Runlease gives people shell access to machines in their cloud accounts. We treat a security
report as the most important kind of issue there is.

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub:
[github.com/marshal-org/runlease/security/advisories/new](https://github.com/marshal-org/runlease/security/advisories/new)

If you cannot use GitHub, email `security@marshal.codes`.

Useful things to include:

- What an attacker can do, and what they need to already have (a relay account, a slot token,
  network position, access to the cloud project).
- The version (`runlease version`) and which cloud.
- Steps to reproduce, or a proof of concept.

## What to expect

| When | What |
|---|---|
| Within 2 working days | We confirm we received the report |
| Within 7 days | We tell you whether we can reproduce it and how serious we think it is |
| Within 30 days | A fix is released for confirmed issues, sooner for critical ones |
| At release | A GitHub Security Advisory is published, with credit to you unless you prefer otherwise |

We will keep you informed while we work on it, and we will not take legal action against anyone
who reports in good faith and follows this policy.

## Scope

In scope:

- The CLI, the agent and the relay in this repository.
- The boot script Runlease generates for a VM.
- The release pipeline and the published binaries.

Particularly interesting to us, because they are the guarantees the project makes:

- Any way for a relay, or someone who has taken one over, to read or change session traffic,
  impersonate a VM, or run code on a VM.
- Any way to reach a VM's shell without the login key.
- Any way to stop a VM from deleting itself at the end of its lease without the owner's action.
- Any way for one relay user to reach another user's slot.

Out of scope:

- Findings that need an attacker who already controls your machine or already has admin rights in
  your cloud account.
- The behaviours listed under [known limits](docs/security.md#known-limits). Ideas for closing
  them are welcome as ordinary issues.
- Denial of service against a relay you do not operate. Please do not test this on shared relays.

## Supported versions

Runlease is pre-1.0. Security fixes go into the latest release only.

## How the design holds up

The [threat model](docs/security.md) describes what each party can and cannot do, with pointers
into the code.
