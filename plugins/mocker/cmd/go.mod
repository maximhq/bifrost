module github.com/maximhq/bifrost/plugins/mocker/cmd

go 1.24.1

replace github.com/maximhq/bifrost/core => ../../../core

replace github.com/maximhq/bifrost/sdk => ../../../sdk

replace github.com/maximhq/bifrost/plugins/mocker => ..

require (
	github.com/maximhq/bifrost/plugins/mocker v0.0.0-00010101000000-000000000000
	github.com/maximhq/bifrost/sdk v0.0.0-00010101000000-000000000000
)

require (
	github.com/fatih/color v1.7.0 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/hashicorp/go-hclog v0.14.1 // indirect
	github.com/hashicorp/go-plugin v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/mattn/go-colorable v0.1.4 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/maximhq/bifrost/core v1.1.4 // indirect
	github.com/oklog/run v1.0.0 // indirect
	golang.org/x/net v0.39.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230711160842-782d3b101e98 // indirect
	google.golang.org/grpc v1.58.3 // indirect
	google.golang.org/protobuf v1.36.1 // indirect
)
