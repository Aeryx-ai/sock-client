module github.com/guygrigsby/sock-client

go 1.26.2

replace github.com/guygrigsby/sock-contract => ../contract

require (
	connectrpc.com/connect v1.21.0
	github.com/guygrigsby/sock-contract v0.3.1
)

require google.golang.org/protobuf v1.36.12 // indirect
