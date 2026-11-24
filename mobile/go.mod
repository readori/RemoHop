// The mobile apps' Go module: the package gomobile binds (remohopcore) and
// the gomobile/gobind tools, kept out of the main module so that remohop does
// not depend on golang.org/x/mobile. It builds against the RemoHop checkout
// it sits in.
module github.com/readori/RemoHop/mobile

go 1.26.0

require github.com/readori/RemoHop v0.0.0

require (
	filippo.io/cpace v0.0.0-20210101143347-24d601e2e469 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/ebitengine/purego v0.11.3 // indirect
	github.com/gtank/ristretto255 v0.1.2 // indirect
	github.com/hanwen/go-fuse/v2 v2.12.1 // indirect
	github.com/jezek/xgb v1.3.1 // indirect
	github.com/quic-go/quic-go v0.65.0 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/mobile v0.0.0-20261117202452-47699113848a // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/term v0.48.0 // indirect
	golang.org/x/tools v0.53.0 // indirect
)

replace github.com/readori/RemoHop => ../

tool (
	golang.org/x/mobile/cmd/gobind
	golang.org/x/mobile/cmd/gomobile
)
