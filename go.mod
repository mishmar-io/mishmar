module github.com/mishmar-io/mishmar

go 1.26.3

toolchain go1.26.10

require (
	github.com/bytemare/ecc v0.10.0
	github.com/bytemare/frost v0.0.0-20260616005703-316cadd937fd
	github.com/bytemare/secret-sharing v0.8.1-0.20260608010144-f8126702fd4d
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1
	github.com/oapi-codegen/runtime v1.7.0
	github.com/shopspring/decimal v1.6.0
	github.com/spf13/cobra v1.10.2
	github.com/vmihailenco/msgpack/v5 v5.4.1
	golang.org/x/crypto v0.58.0
)

require github.com/bytemare/dkg v0.0.0-20260615011009-183ce3f7e6d0

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	filippo.io/nistec v0.0.4 // indirect
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/bytemare/hash v0.6.3 // indirect
	github.com/bytemare/secp256k1 v0.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gtank/ristretto255 v0.2.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mishmar-io/cb-mpc-go v0.0.0-00010101000000-000000000000
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/mishmar-io/cb-mpc-go => ../cb-mpc-go
