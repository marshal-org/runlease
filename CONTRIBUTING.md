# Contributing to Runlease

Thanks for wanting to help. This page is the whole process.

## Ways to contribute

- **Report a bug.** Use the bug template. If a VM was left running, delete it first.
- **Report a vulnerability.** Privately. See [SECURITY.md](SECURITY.md).
- **Add a cloud.** The [provider interface](pkg/provider/interface.go) is four methods. Azure,
  Hetzner and DigitalOcean are all open. Open an issue first so two people do not build the same
  one.
- **Add a recipe.** A manifest that sets up something useful: a k3s cluster, a GPU box, a CI
  runner. Put it under `examples/manifests/` with a comment block explaining it.
- **Improve the docs.** If something confused you, it will confuse the next person.

For anything larger than a small fix, open an issue before you write code. It is quicker for
everyone than a pull request that goes in a direction we cannot merge.

## Getting set up

You need Go 1.26 or newer. Nothing else: the tests are pure Go and need no cloud account.

```bash
git clone https://github.com/marshal-org/runlease.git
cd runlease
go build ./...
go test ./...
```

## Before you open a pull request

Run what CI runs:

```bash
gofmt -l .                 # must print nothing
go vet ./...
go test ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Then:

- **Write tests.** New behaviour needs a test. Use `t.TempDir()` and `httptest.NewServer`; the
  `fakeAgent` helper in `pkg/relay/server_test.go` is the way to test relay flows.
- **Changes to the relay, the agent or the boot script need a real VM.** Unit tests are not
  enough. Say in the pull request which cloud you launched on and what you ran.
- **Keep it small.** One logical change per pull request.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/). The type decides the next
version number, so choose it honestly.

```
feat(relay): cap live slots per identity
fix(down): confirm the VM is gone before reporting success
docs(manifest): document agentSha256
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`. A breaking change takes a `!`
after the type and a `BREAKING CHANGE:` footer. Do not edit `CHANGELOG.md` or push tags; the
release tooling does both.

## Sign your work

Every commit needs a `Signed-off-by` line. It certifies the
[Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change or
have the right to submit it under the project's license.

```bash
git commit -s -m "fix(exec): close the session on every error path"
```

## Contributor agreement

Runlease is distributed under the [Functional Source License](LICENSE). For the project to keep
doing that with code it did not write, it needs a license from each contributor. That is what the
[Contributor License Agreement](CLA.md) is. You keep ownership of your work.

To agree, add one line for yourself to [`CONTRIBUTORS.md`](CONTRIBUTORS.md) in your first pull
request. You do this once.

## Security rules for changes

These hold for every change. The pull request template asks you to confirm them.

1. **The relay stays blind.** Nothing may require the relay to hold a key or inspect session
   bytes.
2. **No trust on first use.** Host keys are issued before boot. Do not add
   `InsecureIgnoreHostKey` or anything like it.
3. **No quiet privilege.** A new way to become root on the VM needs an explicit manifest field,
   off by default.
4. **Fail closed.** When a check cannot be made, stop. Do not continue without it.
5. **Do not log secrets.** Slot tokens are logged as a short prefix only.

The reasoning is in [docs/security.md](docs/security.md).

## Review

A maintainer reviews every pull request. Expect a first response within two working days. Changes
under `pkg/relay/`, `pkg/userdata/`, `pkg/credential/` and `.github/` get a slower, more careful
read.

The first workflow run for a new contributor waits for a maintainer to approve it. That is a
safety setting, not a judgement on your change.

## Conduct

Be kind and assume good faith. The [Code of Conduct](CODE_OF_CONDUCT.md) applies everywhere the
project does.
