# Contributing

sock-client is the Go library Sock's command line tools use to talk to the hub: config and credential loading, sessions, puppets, retries and the browser sign-in flow in `login/`. [sock-cli](https://github.com/Aeryx-ai/sock-cli) and [sock-mcp](https://github.com/Aeryx-ai/sock-mcp) import it, so its exported API is a contract.

## Before you start

- Open an issue before anything beyond a small fix (a typo, a doc correction, an obvious bug). API additions and behavior changes need agreement first.
- Report vulnerabilities privately as [SECURITY.md](SECURITY.md) describes. Never in an issue, pull request or discussion.

## Dev setup

- Go 1.26.9 or newer (`go.mod`). CI reads the version from `go.mod`.
- golangci-lint v2.13.2, the version CI installs:

  ```sh
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
  ```

Linters beyond the defaults are set in `.golangci.yml`: `errorlint`, `intrange`, `misspell`, `revive`, `unparam`.

| Target | Does |
|---|---|
| `make` / `make build` | `go build ./...` |
| `make test` | `go test -race ./...` |
| `make fmt` | Fails if `gofmt -l .` lists any file |
| `make vet` | `go vet ./...` for the host platform |
| `make cross` | `go vet ./...` with `GOOS` set to `darwin`, `linux` and `windows` |
| `make lint` | `golangci-lint run` |
| `make check` | `fmt`, `vet`, `cross`, `lint`, `test` |

`make install` and `make redeploy` print a note and do nothing: this is a library.

## Working across repos

`go.mod` pins [sock-contract](https://github.com/Aeryx-ai/sock-contract) to a public tag and the Go module proxy fetches it. To develop against a local contract checkout, use an untracked `go.work` (already in `.gitignore`):

```sh
go work init .
go work use ../sock-contract
```

To try a client change inside sock-cli or sock-mcp, do the same from that repo:

```sh
cd ../sock-cli
go work init .
go work use ../sock-client ../sock-contract
```

Never commit a `go.work` or a `replace` directive. CI has neither, so before opening a PR check that the change builds against the pinned tags:

```sh
GOWORK=off make check
```

If the change needs something not yet in a contract tag, the contract change ships and gets tagged first. Then bump the pin in the change that first uses it:

```sh
go get github.com/aeryx-ai/sock-contract@vX.Y.Z
go mod tidy
```

## Making a change

1. Fork, then branch from `main`.
2. Write the test with the code. A bug fix starts with a test that fails without the fix. Tests sit next to the code they cover and run with the race detector (`make test`).
3. Parsers of untrusted input have fuzz tests: `parse_fuzz_test.go` in the root package and `login/login_fuzz_test.go`. Extend them when you change that code and run the target you touched, one at a time:

   ```sh
   go test -run '^$' -fuzz '^FuzzRetryAfter$' -fuzztime 60s .
   go test -run '^$' -fuzz '^FuzzCallback$' -fuzztime 60s ./login
   ```

   A failing input is written under the package's `testdata/fuzz/<FuzzName>/`. Fix the bug and commit that file: plain `go test` replays it from then on.
4. Run `make check`. It must pass. CI runs the same target.
5. Keep a PR to one change.

CI skips pull requests that touch only Markdown or `docs/`.

## Public API and semver

Exported identifiers in the root package and `login/` are used by other repos. Treat them as stable:

- Additions are fine: a new function, a new field on a config struct, a new error value.
- Do not remove, rename or change the signature of an exported identifier in a patch release. Mark it `// Deprecated: use X instead.` first and remove it in a later minor release.
- While the module is v0, a breaking change takes a minor version bump and must be called out in the PR. After v1 it takes a new major version and module path.
- Errors callers match with `errors.Is` or `errors.As` are part of the API. Wrap with `%w`; do not swap a sentinel for a string.

## Platforms

sock-client ships on darwin, linux and windows. Platform specific code goes in build constrained files (`lock_unix.go` and `lock_windows.go`, `device_unix.go` and `device_other.go`, `login/listen_unix.go` and `login/listen_windows.go`). `make cross` vets every platform so a unix only call fails before CI. Run it after touching any file that calls the OS.

## Style

- gofmt, `go vet` and golangci-lint clean.
- `for i := range n` rather than a three clause loop for a plain count.
- Wrap errors with context. Small packages.
- Never log or print a token, key or other secret.

## Commit messages

- Imperative subject, short (about 50 characters, 72 at most), no trailing period: `Retry on Retry-After in HTTP date form`.
- A body that explains why, not what the diff already shows.
- One logical change per commit.
- Signed commits are encouraged: `git commit -S` or `git config commit.gpgsign true`. GPG and SSH keys both work.

## Pull requests

- Link the issue it resolves.
- Say what changed and why. Call out any change to the exported API and what callers need to do.
- Include the tests.
- CI must be green before review. A maintainer reviews every PR. Push review fixes as new commits; the maintainer may squash on merge.

## Releases (maintainers)

A release is a signed annotated `vX.Y.Z` tag on `main`, with the version as its message:

```sh
git tag -s v0.2.1 -m v0.2.1
git push origin v0.2.1
```

There is no release workflow. The tag is the release: consumers fetch it through the Go module proxy and bump their pin with `go get github.com/aeryx-ai/sock-client@vX.Y.Z`. Tag only a `main` that passes `make check`.

## License

MIT, copyright Aeryx Industries LLC ([LICENSE](LICENSE)). Inbound = outbound: by contributing you agree your contribution is licensed under the same terms.
