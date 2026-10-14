module github.com/guygrigsby/sock-client

go 1.26.9

replace github.com/guygrigsby/sock-contract => ../contract

require (
	connectrpc.com/connect v1.21.0
	github.com/guygrigsby/sock-contract v0.5.0
)

require (
	github.com/BurntSushi/toml v1.6.0
	google.golang.org/protobuf v1.36.12 // indirect
)
