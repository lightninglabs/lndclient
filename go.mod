module github.com/lightninglabs/lndclient

require (
	github.com/btcsuite/btcd v0.22.0-beta.0.20220316175102-8d5c75c28923
	github.com/btcsuite/btcd/btcec/v2 v2.1.3
	github.com/btcsuite/btcd/btcutil v1.1.1
	github.com/btcsuite/btcd/chaincfg/chainhash v1.0.1
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcwallet/wtxmgr v1.5.0
	github.com/lightningnetwork/lnd v0.14.1-beta.0.20220325230756-dceb10144f71
	github.com/lightningnetwork/lnd/kvdb v1.3.1
	github.com/stretchr/testify v1.7.0
	go.etcd.io/bbolt v1.3.6
	google.golang.org/grpc v1.38.0
	gopkg.in/macaroon-bakery.v2 v2.0.1
	gopkg.in/macaroon.v2 v2.1.0
)

go 1.16

// TODO - replace after merge of guggero/lnd/musig2
// This replace points us to a version of lnd with musig apis.
// We need this to be able to use musig apis.
replace github.com/lightningnetwork/lnd => github.com/guggero/lnd v0.11.0-beta.rc4.0.20220330122225-8e3b1fc06e80

// TODO - replace after merge of roasbeef/btcd/musig2
// This replace points to the version of btcd with musig logic in it.
// We need this because the above LND dependency has the same replace.
replace github.com/btcsuite/btcd/btcec/v2 => github.com/roasbeef/btcd/btcec/v2 v2.0.0-20220331035954-96892acd3872
