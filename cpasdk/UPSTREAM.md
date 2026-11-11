# Vendored CLIProxyAPI SDK

Source: https://github.com/router-for-me/CLIProxyAPI

Upstream commit: `fa11a573b252b30256bef4de952a95feee3c8c9d`

Vendored files:

- `pluginapi/types.go` from `sdk/pluginapi/types.go`
- `pluginabi/types.go` from `sdk/pluginabi/types.go`

Update both files together and re-run `go test ./...` plus the CGO `c-shared` build.
