module github.com/kamichidu/go-nando/adapters/webdav

go 1.25.1

require (
	github.com/kamichidu/go-flagshim v0.0.0-20260706215804-c7cec50d3e31
	github.com/kamichidu/go-nando v0.0.0
	golang.org/x/net v0.33.0
)

require github.com/BurntSushi/toml v1.6.0 // indirect

replace github.com/kamichidu/go-nando => ../..
