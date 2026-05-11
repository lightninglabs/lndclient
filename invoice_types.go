package lndclient

import (
	"github.com/lightningnetwork/lnd/graph/db/models"
)

// CircuitKey is lnd's unique identifier for an incoming HTLC.
type CircuitKey = models.CircuitKey

// ContractState describes the current state of an invoice. The values match
// lnd's invoice contract states.
type ContractState = uint8

const (
	// ContractOpen means the invoice has only been created.
	ContractOpen ContractState = 0

	// ContractSettled means the invoice has been settled.
	ContractSettled ContractState = 1

	// ContractCanceled means the invoice has been canceled.
	ContractCanceled ContractState = 2

	// ContractAccepted means the HTLC has been accepted but not settled.
	ContractAccepted ContractState = 3
)
