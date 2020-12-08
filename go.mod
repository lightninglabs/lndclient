module github.com/lightninglabs/lndclient

go 1.13

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20201114000516-e9c7a5ac6401
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.2
	github.com/btcsuite/btcwallet/wtxmgr v1.2.0
	//TODO(carla): bump to 0.12 once cut.
	github.com/lightningnetwork/lnd v0.11.0-beta.rc4.0.20201208091650-669663bfc22a
	github.com/stretchr/testify v1.5.1
	google.golang.org/grpc v1.24.0
	gopkg.in/macaroon.v2 v2.1.0
)
