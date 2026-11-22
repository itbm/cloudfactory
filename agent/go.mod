module github.com/itbm/cloudfactory/agent

go 1.26.0

toolchain go1.27.4

require (
	connectrpc.com/connect v1.21.1
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/sys v0.50.0

// zstd for the backup archives of other panels (internal/migrate).
require github.com/klauspost/compress v1.20.5
