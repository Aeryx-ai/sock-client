module github.com/aeryx-ai/sock-client

go 1.26.9

replace github.com/aeryx-ai/sock-contract => ../contract

require (
	connectrpc.com/connect v1.21.0
	github.com/aeryx-ai/sock-contract v0.5.0
)

require (
	github.com/BurntSushi/toml v1.6.0
	google.golang.org/protobuf v1.36.12 // indirect
)
