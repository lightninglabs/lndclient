module github.com/lightninglabs/lndclient

require (
	github.com/btcsuite/btcd v0.22.0-beta.0.20211005184431-e3449998be39
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.3-0.20210527170813-e2ba6805a890
	github.com/btcsuite/btcwallet/wtxmgr v1.3.1-0.20210822222949-9b5a201c344c
	github.com/lightningnetwork/lnd v0.14.2-beta
	github.com/lightningnetwork/lnd/kvdb v1.3.0
	github.com/stretchr/testify v1.7.0
	go.etcd.io/bbolt v1.3.6
	google.golang.org/grpc v1.38.0
	gopkg.in/macaroon-bakery.v2 v2.0.1
	gopkg.in/macaroon.v2 v2.1.0
)

go 1.15
