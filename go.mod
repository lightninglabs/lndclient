module github.com/lightninglabs/lndclient

go 1.15

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210426180113-7eba688b65e5
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.3-0.20201208143702-a53e38424cce
	github.com/btcsuite/btcwallet/wtxmgr v1.2.1-0.20210312232944-4ec908df9386
	//TODO(guggero): bump to 0.13 once cut.
	github.com/lightningnetwork/lnd v0.12.0-beta.rc6.0.20210505030502-6d661334599f
	github.com/stretchr/testify v1.5.1
	google.golang.org/grpc v1.29.1
	gopkg.in/macaroon.v2 v2.1.0
)

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.5.0-alpha.5.0.20201125193152-8a03d2e9614b
