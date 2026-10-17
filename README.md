# sock-client

Go client plumbing for the [sock](https://github.com/aeryx-ai/sock-contract) API, shared by sock-cli and sock-mcp: config validation (`Validate`), an authenticated Connect transport that sends the token only to the configured host and never follows redirects (`New`), retries that honor `Retry-After` (`Retrier`, `Call`) and actionable error text (`Describe`, `Reason`, `Transient`).

Consumers build against it with a `replace` to `../client` until it is tagged.

MIT licensed.
