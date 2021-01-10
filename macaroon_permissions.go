package lndclient

import (
	"context"
	"encoding/hex"
	"fmt"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon.v2"
)

func loadAvailablePermissions(macPouch *macaroonPouch) *availablePermissions {
	return &availablePermissions{
		lightning:     checkMacaroonPermissions(macPouch.adminMac, lightningRequiredPermissions),
		walletKit:     checkMacaroonPermissions(macPouch.walletKitMac, walletKitRequiredPermissions),
		invoices:      checkMacaroonPermissions(macPouch.invoiceMac, invoicesRequiredPermissions),
		signer:        checkMacaroonPermissions(macPouch.signerMac, signerRequiredPermissions),
		chainNotifier: checkMacaroonPermissions(macPouch.chainMac, chainNotifierRequiredPermissions),
		router:        checkMacaroonPermissions(macPouch.routerMac, routerRequiredPermissions),
		readOnly:      checkMacaroonPermissions(macPouch.readonlyMac, readOnlyRequiredPermssions),
	}
}

// checkMacaroonPermissions takes a serializedMacaroon
// and checks that it has all of the required permissions for
// a given client.
// Returns false and an error if an error occurs while loading
// the macaroon data or creating a bakery.Oven.
func checkMacaroonPermissions(mac serializedMacaroon,
	requiredPermissions []bakery.Op) bool {

	m, err := unmarshalMacaroon(mac)
	if err != nil {
		log.Error(err)
		return false
	}

	macOven := bakery.NewOven(bakery.OvenParams{})

	ops, _, err := macOven.VerifyMacaroon(context.Background(), []*macaroon.Macaroon{m})
	if err != nil {
		log.Error(err)
		return false
	}

	macOpsMap := convertMacOpsToMap(ops)
	requiredPermsMap := convertMacOpsToMap(requiredPermissions)
	permissionsMap := make(map[string]bool)

	// appends all matched permissions in macOpsMap
	// to permissionsMap
	for permName := range requiredPermsMap {
		if _, ok := macOpsMap[permName]; ok {
			permissionsMap[permName] = true
		}
	}

	hasAllPermissions := len(permissionsMap) == len(requiredPermissions)

	return hasAllPermissions
}

// convertMacOpsToMap converts a slice of bakery.Op into a map[string]bool
// by concatenating the entity name and action into a single string;
// for example, { Entity: "address", Action: "read" } becomes "address.read".
func convertMacOpsToMap(ops []bakery.Op) map[string]bool {
	opsMap := make(map[string]bool)

	for _, op := range ops {
		macOp := fmt.Sprintf("%[1]s.%[2]s", op.Entity, op.Action)

		_, ok := opsMap[macOp]
		if !ok {
			opsMap[macOp] = true
		}
	}

	return opsMap
}

func unmarshalMacaroon(mac serializedMacaroon) (*macaroon.Macaroon, error) {
	var m = &macaroon.Macaroon{}

	deserializedMac, err := hex.DecodeString(string(mac))
	if err != nil {
		return nil, fmt.Errorf("could not deserialize macaroon: %[1]v", err)
	}

	if err := m.UnmarshalBinary(deserializedMac); err != nil {
		return nil, fmt.Errorf("could not unmarshal macaroon data: %[1]v", err)
	}

	return m, nil
}
