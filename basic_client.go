package lndclient

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/lightningnetwork/lnd/lncfg"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	macaroon "gopkg.in/macaroon.v2"
)

// BasicClientOption is a functional option argument that allows adding
// arbitrary lnd basic client configuration overrides, without forcing existing
// users of NewBasicClient to update their invocation. These are always
// processed in order, with later options overriding earlier ones.
type BasicClientOption func(*basicClientOptions)

// basicClientOptions is a set of options that can configure the lnd client
// returned by NewBasicClient.
type basicClientOptions struct {
	macFilename string
	tlsData     string
	macData     string
	insecure    bool
	systemCerts bool
}

// defaultBasicClientOptions returns a basicClientOptions set to lnd basic
// client defaults.
func defaultBasicClientOptions() *basicClientOptions {
	return &basicClientOptions{
		macFilename: string(AdminServiceMac),
	}
}

// MacFilename is a basic client option that sets the name of the macaroon file
// to use.
func MacFilename(macFilename string) BasicClientOption {
	return func(bc *basicClientOptions) {
		bc.macFilename = macFilename
	}
}

// TLSData is a basic client option that sets TLS data (encoded in PEM format)
// directly instead of loading them from a file.
func TLSData(tlsData string) BasicClientOption {
	return func(bc *basicClientOptions) {
		bc.tlsData = tlsData
	}
}

// MacaroonData is a basic client option that sets macaroon data (encoded as hex
// string) directly instead of loading it from a file.
func MacaroonData(macData string) BasicClientOption {
	return func(bc *basicClientOptions) {
		bc.macData = macData
	}
}

// Insecure allows the basic client to establish an insecure (non-TLS)
// connection to the RPC server.
func Insecure() BasicClientOption {
	return func(bc *basicClientOptions) {
		bc.insecure = true
	}
}

// SystemCerts instructs the basic client to use the system's certificate trust
// store for verifying the TLS certificate of the RPC server.
func SystemCerts() BasicClientOption {
	return func(bc *basicClientOptions) {
		bc.systemCerts = true
	}
}

// applyBasicClientOptions updates a basicClientOptions set with functional
// options.
func (bc *basicClientOptions) applyBasicClientOptions(
	options ...BasicClientOption) {

	for _, option := range options {
		option(bc)
	}
}

// NewBasicClient creates a new basic gRPC client to lnd. We call this client
// "basic" as it falls back to expected defaults if the arguments aren't
// provided.
func NewBasicClient(lndHost, tlsPath, macDir, network string,
	basicOptions ...BasicClientOption) (lnrpc.LightningClient, error) {

	conn, err := NewBasicConn(
		lndHost, tlsPath, macDir, network, basicOptions...,
	)
	if err != nil {
		return nil, err
	}

	return lnrpc.NewLightningClient(conn), nil
}

// NewBasicConn creates a new basic gRPC connection to lnd. We call this
// connection "basic" as it falls back to expected defaults if the arguments
// aren't provided.
func NewBasicConn(lndHost string, tlsPath, macDir, network string,
	basicOptions ...BasicClientOption) (*grpc.ClientConn, error) {

	creds, mac, err := parseTLSAndMacaroon(
		tlsPath, macDir, network, basicOptions...,
	)
	if err != nil {
		return nil, err
	}

	// Now we append the macaroon credentials to the dial options.
	cred, err := macaroons.NewMacaroonCredential(mac)
	if err != nil {
		return nil, fmt.Errorf("error creating macaroon credential: %v",
			err)
	}

	// Create a dial options array.
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(cred),
		grpc.WithDefaultCallOptions(maxMsgRecvSize),
	}

	// We need to use a custom dialer so we can also connect to unix sockets
	// and not just TCP addresses.
	opts = append(
		opts, grpc.WithContextDialer(
			lncfg.ClientAddressDialer(defaultRPCPort),
		),
	)
	conn, err := grpc.Dial(lndHost, opts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %v",
			err)
	}

	return conn, nil
}

// parseTLSAndMacaroon looks to see if the TLS certificate and macaroon were
// passed in as a path or as straight-up data, and processes it accordingly, so
// it can be passed into grpc to establish a connection with LND.
func parseTLSAndMacaroon(tlsPath, macDir, network string,
	basicOptions ...BasicClientOption) (credentials.TransportCredentials,
	*macaroon.Macaroon, error) {

	// Starting with the set of default options, we'll apply any specified
	// functional options to the basic client.
	bco := defaultBasicClientOptions()
	bco.applyBasicClientOptions(basicOptions...)

	creds, err := GetTLSCredentials(
		bco.tlsData, tlsPath, bco.insecure, bco.systemCerts,
	)
	if err != nil {
		return nil, nil, err
	}

	var macBytes []byte
	mac := &macaroon.Macaroon{}
	if bco.macData != "" {
		macBytes, err = hex.DecodeString(bco.macData)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if macDir == "" {
			macDir = filepath.Join(
				defaultLndDir, defaultDataDir, defaultChainSubDir,
				"bitcoin", network,
			)
		}

		macPath := filepath.Join(macDir, bco.macFilename)

		// Load the specified macaroon file.
		macBytes, err = ioutil.ReadFile(macPath)
		if err != nil {
			return nil, nil, err
		}
	}

	if err = mac.UnmarshalBinary(macBytes); err != nil {
		return nil, nil, fmt.Errorf("unable to decode macaroon: %v",
			err)
	}

	return creds, mac, nil
}
