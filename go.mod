module github.com/lightninglabs/lndclient

require (
	github.com/btcsuite/btcd v0.23.3
	github.com/btcsuite/btcd/btcec/v2 v2.2.1
	github.com/btcsuite/btcd/btcutil v1.1.2
	github.com/btcsuite/btcd/btcutil/psbt v1.1.5
	github.com/btcsuite/btcd/chaincfg/chainhash v1.0.1
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcwallet/wtxmgr v1.5.0
	github.com/lightningnetwork/lnd v0.15.0-beta.rc6.0.20221104092723-22fec76339a7
	github.com/lightningnetwork/lnd/kvdb v1.3.1
	github.com/stretchr/testify v1.8.1
	go.etcd.io/bbolt v1.3.6
	google.golang.org/grpc v1.53.0
	gopkg.in/macaroon-bakery.v2 v2.0.1
	gopkg.in/macaroon.v2 v2.1.0
)

require (
	github.com/juju/errors v0.0.0-20220331221717-b38fca44723b // indirect
	github.com/juju/retry v0.0.0-20220204093819-62423bf33287 // indirect
	github.com/juju/version/v2 v2.0.0-20220204124744-fc9915e3d935 // indirect
	github.com/prometheus/client_golang v1.11.1 // indirect
	golang.org/x/crypto v0.1.0 // indirect
	golang.org/x/net v0.7.0 // indirect
)

go 1.16
