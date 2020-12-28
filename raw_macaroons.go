package lndclient

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// RawMacaroons contains raw []byte macaroons
// for later hex encoding and use with LND.
type RawMacaroons struct {
	AdminMac     []byte
	InvoiceMac   []byte
	ChainMac     []byte
	SignerMac    []byte
	WalletKitMac []byte
	RouterMac    []byte
	ReadOnlyMac  []byte
}

// Returns a RawMacaroons object populated with raw []byte macaroons.
// Best used if loading macaroons directly from files, or if reading from
// Kubernetes secrets containing raw byte data (for example)
func NewRawMacaroons(adminMac []byte, invoiceMac []byte,
	chainMac []byte, signerMac []byte, walletKitMac []byte,
	routerMac []byte, readOnlyMac []byte) *RawMacaroons {

	return &RawMacaroons{
		AdminMac:     adminMac,
		InvoiceMac:   invoiceMac,
		ChainMac:     chainMac,
		SignerMac:    signerMac,
		WalletKitMac: walletKitMac,
		RouterMac:    routerMac,
		ReadOnlyMac:  readOnlyMac,
	}
}

// Returns a RawMacaroons object populated with []byte data
// from base64-encoded macaroons.
// Note: if an error occurs when attempting to decode a base64 string,
// a nil object is returned along with the decode error.
func NewRawMacaroonsFromBase64(adminMac string, invoiceMac string,
	chainMac string, signerMac string, walletKitMac string,
	routerMac string, readOnlyMac string) (*RawMacaroons, error) {

	var rawMacs = &RawMacaroons{}

	macaroonMap := map[string]string{
		"admin":     adminMac,
		"invoice":   invoiceMac,
		"chain":     chainMac,
		"signer":    signerMac,
		"walletkit": walletKitMac,
		"router":    routerMac,
		"readonly":  readOnlyMac,
	}

	for macName, encodedData := range macaroonMap {
		decoded, err := decodeBase64Macaroon(encodedData)
		if err != nil {
			return nil, fmt.Errorf("error decoding base64 data for "+
				"%[1]s macaroon: %[2]v", macName, err)
		}

		// I do apologize for this.
		switch macName {
		case "admin":
			rawMacs.AdminMac = decoded
		case "invoice":
			rawMacs.InvoiceMac = decoded
		case "chain":
			rawMacs.ChainMac = decoded
		case "signer":
			rawMacs.SignerMac = decoded
		case "walletkit":
			rawMacs.WalletKitMac = decoded
		case "router":
			rawMacs.RouterMac = decoded
		case "readonly":
			rawMacs.ReadOnlyMac = decoded
		}
	}

	return rawMacs, nil
}

// Returns a RawMacaroons object whose macaroons are only
// the lnd admin macaroon.
func NewRawMacaroonsFromAdmin(adminMac []byte) *RawMacaroons {
	return &RawMacaroons{
		AdminMac:     adminMac,
		InvoiceMac:   adminMac,
		ChainMac:     adminMac,
		SignerMac:    adminMac,
		WalletKitMac: adminMac,
		RouterMac:    adminMac,
		ReadOnlyMac:  adminMac,
	}
}

// Returns a RawMacaroons object whose macaroons are only
// the lnd admin macaroon.
func NewRawMacaroonsFromAdminBase64(adminMac string) (*RawMacaroons, error) {
	decoded, err := decodeBase64Macaroon(adminMac)
	if err != nil {
		return nil, fmt.Errorf("error decoding base64 data for "+
			"admin macaroon: %[1]v", err)
	}

	return &RawMacaroons{
		AdminMac:     decoded,
		InvoiceMac:   decoded,
		ChainMac:     decoded,
		SignerMac:    decoded,
		WalletKitMac: decoded,
		RouterMac:    decoded,
		ReadOnlyMac:  decoded,
	}, nil
}

// Converts raw byte macaroons into hex-encoded serializedMacaroon data.
func (r *RawMacaroons) toMacaroonPouch() *macaroonPouch {
	return &macaroonPouch{
		adminMac:     serializeBytesToMacaroon(r.AdminMac),
		invoiceMac:   serializeBytesToMacaroon(r.InvoiceMac),
		chainMac:     serializeBytesToMacaroon(r.ChainMac),
		signerMac:    serializeBytesToMacaroon(r.SignerMac),
		walletKitMac: serializeBytesToMacaroon(r.WalletKitMac),
		routerMac:    serializeBytesToMacaroon(r.RouterMac),
		readonlyMac:  serializeBytesToMacaroon(r.ReadOnlyMac),
	}
}

// Verifies that none of the macaroons in a RawMacaroons
// object are nil bytes.
func (r *RawMacaroons) verifyMacaroons() error {
	macaroonMap := map[string][]byte{
		"admin":     r.AdminMac,
		"invoice":   r.InvoiceMac,
		"chain":     r.ChainMac,
		"signer":    r.SignerMac,
		"walletkit": r.WalletKitMac,
		"router":    r.RouterMac,
		"readonly":  r.ReadOnlyMac,
	}

	for macName, macData := range macaroonMap {
		if macData == nil {
			return fmt.Errorf("no %s macaroon data found", macName)
		}
	}

	return nil
}

func serializeBytesToMacaroon(data []byte) serializedMacaroon {
	var serialized serializedMacaroon

	if data == nil {
		serialized = ""
	} else {
		serialized = serializedMacaroon(hex.EncodeToString(data))
	}

	return serialized
}

func decodeBase64Macaroon(data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(data)
}
