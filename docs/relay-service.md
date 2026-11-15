# The public relay

There is a relay you can use without running one: `wss://connect.runlease.online`. It is free,
it needs a Google account, and it is limited. This page says what you get, what it sees, and
when to run your own instead.

---

## Use it

```bash
runlease config set relay.addr wss://connect.runlease.online
runlease login
```

`runlease login` prints a short code and a link. Open the link in any browser, on any device,
enter the code and approve. Nothing has to open on the machine you are working on, so it works
over SSH, in a container, and for an agent whose human is at another screen.

```bash
runlease whoami     # who you are, your tier, how much of it is in use
runlease up         # launch as usual
runlease logout     # forget the saved sign-in
```

The sign-in is saved in `~/.runlease/auth.json` and refreshed automatically.

## What the free tier allows

| | Free |
|---|---|
| Machines connected at once | 2 |
| Longest a machine can stay connected | 8 hours, then run `runlease connect` to reconnect |
| A machine that has been gone for 15 minutes | Its place is given back |
| Price | Nothing |
| Uptime promise | None |

`runlease down` gives a place back at once. If you reach the limit, `runlease up` tells you so
and tells you which command frees a place.

The limits apply to the relay only. The machines are yours, in your cloud account, on your bill;
Runlease does not limit or meter them.

Larger tiers may be offered later. The limits above can change; `runlease whoami` and
`https://connect.runlease.online/.well-known/runlease-relay` always show the current ones.

## What it sees

The relay is built so that it does not have to be trusted with your sessions. The
[threat model](security.md) has the full account. In short:

**It never sees** your SSH keys, your cloud credentials, your commands, your files or the
contents of forwarded connections. Sessions are SSH from your machine to your VM; the relay
carries ciphertext.

**It does see and log:**

- the email address of the Google account you signed in with, to apply your tier and to deal
  with abuse;
- when you open and close a slot, and the first characters of its token;
- the network addresses that connect to it, and how much data passes.

Sign-in asks Google for your email address and nothing else. See the
[privacy notice](https://runlease.online/privacy.html).

## Acceptable use

The relay exists to reach machines you launched with Runlease. Do not use it as a general
tunnel, to hide the origin of traffic, or to carry anything illegal. Accounts that do are
removed. Full terms: [runlease.online/terms.html](https://runlease.online/terms.html).

## When to run your own

Run your own relay when any of these is true:

- you need more than the free limits, or an uptime you can rely on;
- you do not want a third party to see who connects to what, and when;
- your machines must only talk to hosts inside your own network;
- you want to admit your organisation's identities and nobody else.

It is one small binary. [Self-hosting the relay](self-hosting-relay.md) covers it, and
[`deploy/relay/gcp`](../deploy/relay/gcp) is a Terraform module that stands one up.
