package lndclient

import (
	"context"
	"encoding/hex"
	"io/ioutil"
	"path/filepath"

	"google.golang.org/grpc/metadata"
)

// loadMacaroon tries to load a macaroon file either from the default macaroon
// dir and the default filename or, if specified, from the custom macaroon path
// that overwrites the former two parameters.
func loadMacaroon(defaultMacDir, defaultMacFileName,
	customMacPath string) (serializedMacaroon, error) {

	// If a custom macaroon path is set, we ignore the macaroon dir and
	// default filename and always just load the custom macaroon, assuming
	// it contains all permissions needed to use the subservers.
	if customMacPath != "" {
		return newSerializedMacaroon(customMacPath)
	}

	return newSerializedMacaroon(filepath.Join(
		defaultMacDir, defaultMacFileName,
	))
}

// serializedMacaroon is a type that represents a hex-encoded macaroon. We'll
// use this primarily vs the raw binary format as the gRPC metadata feature
// requires that all keys and values be strings.
type serializedMacaroon string

// newSerializedMacaroon reads a new serializedMacaroon from that target
// macaroon path. If the file can't be found, then an error is returned.
func newSerializedMacaroon(macaroonPath string) (serializedMacaroon, error) {
	macBytes, err := ioutil.ReadFile(macaroonPath)
	if err != nil {
		return "", err
	}

	return serializedMacaroon(hex.EncodeToString(macBytes)), nil
}

// WithMacaroonAuth modifies the passed context to include the macaroon KV
// metadata of the target macaroon. This method can be used to add the macaroon
// at call time, rather than when the connection to the gRPC server is created.
func (s serializedMacaroon) WithMacaroonAuth(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "macaroon", string(s))
}

// macaroonPouch holds the set of macaroons we need to interact with lnd for
// Loop. Each sub-server has its own macaroon, and for the remaining temporary
// calls that directly hit lnd, we'll use the admin macaroon.
type macaroonPouch struct {
	// invoiceMac is the macaroon for the invoices sub-server.
	invoiceMac serializedMacaroon

	// chainMac is the macaroon for the ChainNotifier sub-server.
	chainMac serializedMacaroon

	// signerMac is the macaroon for the Signer sub-server.
	signerMac serializedMacaroon

	// walletKitMac is the macaroon for the WalletKit sub-server.
	walletKitMac serializedMacaroon

	// routerMac is the macaroon for the router sub-server.
	routerMac serializedMacaroon

	// adminMac is the primary admin macaroon for lnd.
	adminMac serializedMacaroon

	// readonlyMac is the primary read-only macaroon for lnd.
	readonlyMac serializedMacaroon
}

// newMacaroonPouch returns a new instance of a fully populated macaroonPouch
// given the directory where all the macaroons are stored.
func newMacaroonPouch(macaroonDir, customMacPath string) (*macaroonPouch,
	error) {

	// If a custom macaroon is specified, we assume it contains all
	// permissions needed for the different subservers to function and we
	// use it for all of them.
	if customMacPath != "" {
		mac, err := loadMacaroon("", "", customMacPath)
		if err != nil {
			return nil, err
		}

		return &macaroonPouch{
			invoiceMac:   mac,
			chainMac:     mac,
			signerMac:    mac,
			walletKitMac: mac,
			routerMac:    mac,
			adminMac:     mac,
			readonlyMac:  mac,
		}, nil
	}

	var (
		m   = &macaroonPouch{}
		err error
	)

	m.invoiceMac, err = loadMacaroon(
		macaroonDir, defaultInvoiceMacaroonFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.chainMac, err = loadMacaroon(
		macaroonDir, defaultChainMacaroonFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.signerMac, err = loadMacaroon(
		macaroonDir, defaultSignerFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.walletKitMac, err = loadMacaroon(
		macaroonDir, defaultWalletKitMacaroonFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.routerMac, err = loadMacaroon(
		macaroonDir, defaultRouterMacaroonFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.adminMac, err = loadMacaroon(
		macaroonDir, defaultAdminMacaroonFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	m.readonlyMac, err = loadMacaroon(
		macaroonDir, defaultReadonlyFilename, customMacPath,
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}
