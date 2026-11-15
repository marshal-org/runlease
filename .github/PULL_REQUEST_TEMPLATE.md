## What and why

<!-- One or two sentences. Link the issue if there is one. -->

## How it was tested

- [ ] `go test ./...`
- [ ] Ran against a real VM (say which cloud, and what you ran)
- [ ] Not applicable (docs or comments only)

<!-- Relay, agent or boot-script changes: unit tests are not enough.
     Say that you deployed the relay, launched a fresh VM, and ran several clients at once. -->

## Security checklist

- [ ] The relay still cannot read or alter session traffic
- [ ] No new way to gain root or sudo on the VM without an explicit manifest opt-in
- [ ] No host key is trusted on first use
- [ ] Nothing secret is logged

## Notes for the reviewer
