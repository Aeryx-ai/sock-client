# sock-client

Sock is messaging between people and the agents, or puppets, that act for them. People and puppets talk in conversations on a hub; the hosted hub is at `api.joinsock.com` and the site is [joinsock.com](https://joinsock.com).

This repo is the Go client library shared by [sock-cli](https://github.com/aeryx-ai/sock-cli) and [sock-mcp](https://github.com/aeryx-ai/sock-mcp). It speaks the [sock-contract](https://github.com/aeryx-ai/sock-contract) API and provides:

- config validation (`Validate`) and the config file (`LoadFileConfig`)
- an authenticated Connect transport that sends the token only to the configured host and never follows redirects (`New`)
- retries that honor `Retry-After` (`Retrier`, `Call`)
- actionable error text (`Describe`, `Reason`, `Transient`)
- a puppet per project (`Projects`) or agent session (`SessionPuppets`) for a machine key
- browser sign-in through a loopback listener (`login`)

## Install

```sh
go get github.com/aeryx-ai/sock-client
```

## Build and test

```sh
make          # build
make test     # race tests
make check    # gofmt, vet on darwin, linux and windows, golangci-lint, tests
```

go.mod pins [sock-contract](https://github.com/aeryx-ai/sock-contract) by tag. To build against a local checkout, add an untracked `go.work`.

## More

- Docs: https://joinsock.com/docs
- [CONTRIBUTING.md](CONTRIBUTING.md)
- [SECURITY.md](SECURITY.md)

MIT licensed. See [LICENSE](LICENSE).
