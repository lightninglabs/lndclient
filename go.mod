module github.com/lightninglabs/lndclient

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210513141527-ee5896bad5be
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.3-0.20210527170813-e2ba6805a890
	github.com/btcsuite/btcwallet/wtxmgr v1.3.1-0.20210706234807-aaf03fee735a
	github.com/lightningnetwork/lnd v0.13.0-beta.rc2
	github.com/stretchr/testify v1.7.0
	google.golang.org/grpc v1.38.0
	gopkg.in/macaroon.v2 v2.1.0
)

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.5.0-alpha.5.0.20201125193152-8a03d2e9614b

replace github.com/lightningnetwork/lnd => github.com/wpaulino/lnd v0.2.1-alpha.0.20210728230947-297dc5461e32

go 1.15
