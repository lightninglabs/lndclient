package lndclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
	"github.com/btcsuite/btcwallet/wtxmgr"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"google.golang.org/grpc"
)

// LeaseDescriptor contains information about a locked output.
type LeaseDescriptor struct {
	// LockID is the ID of the lease.
	LockID wtxmgr.LockID

	// Outpoint is the outpoint of the locked output.
	Outpoint wire.OutPoint

	// Value is the value of the locked output in satoshis.
	Value btcutil.Amount

	// PkScript is the pkscript of the locked output.
	PkScript []byte

	// Expiration is the absolute time of the lock's expiration.
	Expiration time.Time
}

// ListUnspentOption is a functional type for an option that modifies a
// ListUnspentRequest.
type ListUnspentOption func(r *walletrpc.ListUnspentRequest)

// WithUnspentAccount is an option for setting the account on a
// ListUnspentRequest.
func WithUnspentAccount(account string) ListUnspentOption {
	return func(r *walletrpc.ListUnspentRequest) {
		r.Account = account
	}
}

// WithUnspentUnconfirmedOnly is an option for setting the UnconfirmedOnly flag
// on a ListUnspentRequest.
func WithUnspentUnconfirmedOnly() ListUnspentOption {
	return func(r *walletrpc.ListUnspentRequest) {
		r.UnconfirmedOnly = true
	}
}

// WalletKitClient exposes wallet functionality.
type WalletKitClient interface {
	ServiceClient[walletrpc.WalletKitClient]

	// ListUnspent returns a list of all utxos spendable by the wallet with
	// a number of confirmations between the specified minimum and maximum.
	ListUnspent(ctx context.Context, minConfs, maxConfs int32,
		opts ...ListUnspentOption) ([]*lnwallet.Utxo, error)

	// LeaseOutput locks an output to the given ID for the lease time
	// provided, preventing it from being available for any future coin
	// selection attempts. The absolute time of the lock's expiration is
	// returned. The expiration of the lock can be extended by successive
	// invocations of this call. Outputs can be unlocked before their
	// expiration through `ReleaseOutput`.
	LeaseOutput(ctx context.Context, lockID wtxmgr.LockID,
		op wire.OutPoint, leaseTime time.Duration) (time.Time, error)

	// ListLeases returns a list of all currently locked outputs.
	ListLeases(ctx context.Context) ([]LeaseDescriptor, error)

	// ReleaseOutput unlocks an output, allowing it to be available for coin
	// selection if it remains unspent. The ID should match the one used to
	// originally lock the output.
	ReleaseOutput(ctx context.Context, lockID wtxmgr.LockID,
		op wire.OutPoint) error

	DeriveNextKey(ctx context.Context, family int32) (
		*keychain.KeyDescriptor, error)

	DeriveKey(ctx context.Context, locator *keychain.KeyLocator) (
		*keychain.KeyDescriptor, error)

	NextAddr(ctx context.Context, accountName string,
		addressType walletrpc.AddressType,
		change bool) (btcutil.Address, error)

	// GetTransaction returns details for a transaction found in the wallet.
	GetTransaction(ctx context.Context,
		txid chainhash.Hash) (Transaction, error)

	PublishTransaction(ctx context.Context, tx *wire.MsgTx,
		label string) error

	SendOutputs(ctx context.Context, outputs []*wire.TxOut,
		feeRate chainfee.SatPerKWeight,
		label string) (*wire.MsgTx, error)

	EstimateFeeRate(ctx context.Context,
		confTarget int32) (chainfee.SatPerKWeight, error)

	// MinRelayFee returns the current minimum relay fee based on our chain
	// backend in sat/kw.
	MinRelayFee(ctx context.Context) (chainfee.SatPerKWeight, error)

	// ListSweeps returns a list of sweep transaction ids known to our node.
	// Note that this function only looks up transaction ids, and does not
	// query our wallet for the full set of transactions. If startHeight is
	// set to zero it'll fetch all sweeps. If it's set to -1 it'll fetch the
	// pending sweeps only.
	ListSweeps(ctx context.Context, startHeight int32) ([]string, error)

	// ListSweepsVerbose returns a list of sweep transactions known to our
	// node with verbose information about each sweep. If startHeight is set
	// to zero it'll fetch all sweeps. If it's set to -1 it'll fetch the
	// pending sweeps only.
	ListSweepsVerbose(ctx context.Context, startHeight int32) (
		[]lnwallet.TransactionDetail, error)

	// BumpFee attempts to bump the fee of a transaction by spending one of
	// its outputs at the given fee rate. This essentially results in a
	// child-pays-for-parent (CPFP) scenario. If the given output has been
	// used in a previous BumpFee call, then a transaction replacing the
	// previous is broadcast, resulting in a replace-by-fee (RBF) scenario.
	BumpFee(context.Context, wire.OutPoint, chainfee.SatPerKWeight,
		...BumpFeeOption) error

	// ListAccounts retrieves all accounts belonging to the wallet by default.
	// Optional name and addressType can be provided to filter through all the
	// wallet accounts and return only those matching.
	ListAccounts(ctx context.Context, name string,
		addressType walletrpc.AddressType) ([]*walletrpc.Account, error)

	// FundPsbt creates a fully populated PSBT that contains enough inputs
	// to fund the outputs specified in the template. There are two ways of
	// specifying a template: Either by passing in a PSBT with at least one
	// output declared or by passing in a raw TxTemplate message. If there
	// are no inputs specified in the template, coin selection is performed
	// automatically. If the template does contain any inputs, it is assumed
	// that full coin selection happened externally and no additional inputs
	// are added. If the specified inputs aren't enough to fund the outputs
	// with the given fee rate, an error is returned.
	// After either selecting or verifying the inputs, all input UTXOs are
	// locked with an internal app ID.
	//
	// NOTE: If this method returns without an error, it is the caller's
	// responsibility to either spend the locked UTXOs (by finalizing and
	// then publishing the transaction) or to unlock/release the locked
	// UTXOs in case of an error on the caller's side.
	FundPsbt(ctx context.Context,
		req *walletrpc.FundPsbtRequest) (*psbt.Packet, int32,
		[]*walletrpc.UtxoLease, error)

	// SignPsbt expects a partial transaction with all inputs and outputs
	// fully declared and tries to sign all unsigned inputs that have all
	// required fields (UTXO information, BIP32 derivation information,
	// witness or sig scripts) set.
	// If no error is returned, the PSBT is ready to be given to the next
	// signer or to be finalized if lnd was the last signer.
	//
	// NOTE: This RPC only signs inputs (and only those it can sign), it
	// does not perform any other tasks (such as coin selection, UTXO
	// locking or input/output/fee value validation, PSBT finalization). Any
	// input that is incomplete will be skipped.
	SignPsbt(ctx context.Context, packet *psbt.Packet) (*psbt.Packet, error)

	// FinalizePsbt expects a partial transaction with all inputs and
	// outputs fully declared and tries to sign all inputs that belong to
	// the wallet. Lnd must be the last signer of the transaction. That
	// means, if there are any unsigned non-witness inputs or inputs without
	// UTXO information attached or inputs without witness data that do not
	// belong to lnd's wallet, this method will fail. If no error is
	// returned, the PSBT is ready to be extracted and the final TX within
	// to be broadcast.
	//
	// NOTE: This method does NOT publish the transaction once finalized. It
	// is the caller's responsibility to either publish the transaction on
	// success or unlock/release any locked UTXOs in case of an error in
	// this method.
	FinalizePsbt(ctx context.Context, packet *psbt.Packet,
		account string) (*psbt.Packet, *wire.MsgTx, error)

	// ImportPublicKey imports a public key as watch-only into the wallet.
	//
	// NOTE: Events (deposits/spends) for a key will only be detected by lnd
	// if they happen after the import. Rescans to detect past events will
	// be supported later on.
	ImportPublicKey(ctx context.Context, pubkey *btcec.PublicKey,
		addrType lnwallet.AddressType) error

	// ImportTaprootScript imports a user-provided taproot script into the
	// wallet. The imported script will act as a pay-to-taproot address.
	//
	// NOTE: Events (deposits/spends) for a key will only be detected by lnd
	// if they happen after the import. Rescans to detect past events will
	// be supported later on.
	//
	// NOTE: Taproot keys imported through this RPC currently _cannot_ be
	// used for funding PSBTs. Only tracking the balance and UTXOs is
	// currently supported.
	ImportTaprootScript(ctx context.Context,
		tapscript *waddrmgr.Tapscript) (btcutil.Address, error)
}

type walletKitClient struct {
	client       walletrpc.WalletKitClient
	walletKitMac serializedMacaroon
	timeout      time.Duration
	params       *chaincfg.Params
}

// A compile time check to ensure that  walletKitClient implements the
// WalletKitClient interface.
var _ WalletKitClient = (*walletKitClient)(nil)

func newWalletKitClient(conn grpc.ClientConnInterface,
	walletKitMac serializedMacaroon, timeout time.Duration,
	chainParams *chaincfg.Params) *walletKitClient {

	return &walletKitClient{
		client:       walletrpc.NewWalletKitClient(conn),
		walletKitMac: walletKitMac,
		timeout:      timeout,
		params:       chainParams,
	}
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (m *walletKitClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	walletrpc.WalletKitClient) {

	return m.walletKitMac.WithMacaroonAuth(parentCtx), m.timeout, m.client
}

// ListUnspent returns a list of all utxos spendable by the wallet with a number
// of confirmations between the specified minimum and maximum.
func (m *walletKitClient) ListUnspent(ctx context.Context, minConfs,
	maxConfs int32, opts ...ListUnspentOption) ([]*lnwallet.Utxo, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcReq := &walletrpc.ListUnspentRequest{
		MinConfs: minConfs,
		MaxConfs: maxConfs,
	}

	for _, opt := range opts {
		opt(rpcReq)
	}

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.ListUnspent(rpcCtx, rpcReq)
	if err != nil {
		return nil, err
	}

	utxos := make([]*lnwallet.Utxo, 0, len(resp.Utxos))
	for _, utxo := range resp.Utxos {
		var addrType lnwallet.AddressType
		switch utxo.AddressType {
		case lnrpc.AddressType_WITNESS_PUBKEY_HASH:
			addrType = lnwallet.WitnessPubKey
		case lnrpc.AddressType_NESTED_PUBKEY_HASH:
			addrType = lnwallet.NestedWitnessPubKey
		case lnrpc.AddressType_TAPROOT_PUBKEY:
			addrType = lnwallet.TaprootPubkey
		default:
			return nil, fmt.Errorf("invalid utxo address type %v",
				utxo.AddressType)
		}

		pkScript, err := hex.DecodeString(utxo.PkScript)
		if err != nil {
			return nil, err
		}

		opHash, err := chainhash.NewHash(utxo.Outpoint.TxidBytes)
		if err != nil {
			return nil, err
		}

		utxos = append(utxos, &lnwallet.Utxo{
			AddressType:   addrType,
			Value:         btcutil.Amount(utxo.AmountSat),
			Confirmations: utxo.Confirmations,
			PkScript:      pkScript,
			OutPoint: wire.OutPoint{
				Hash:  *opHash,
				Index: utxo.Outpoint.OutputIndex,
			},
		})
	}

	return utxos, nil
}

// LeaseOutput locks an output to the given ID, preventing it from being
// available for any future coin selection attempts. The absolute time of the
// lock's expiration is returned. The expiration of the lock can be extended by
// successive invocations of this call. Outputs can be unlocked before their
// expiration through `ReleaseOutput`.
func (m *walletKitClient) LeaseOutput(ctx context.Context, lockID wtxmgr.LockID,
	op wire.OutPoint, leaseTime time.Duration) (time.Time, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.LeaseOutput(rpcCtx, &walletrpc.LeaseOutputRequest{
		Id: lockID[:],
		Outpoint: &lnrpc.OutPoint{
			TxidBytes:   op.Hash[:],
			OutputIndex: op.Index,
		},
		ExpirationSeconds: uint64(leaseTime.Seconds()),
	})
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(int64(resp.Expiration), 0), nil
}

// ListLeases returns a list of all currently locked outputs.
func (m *walletKitClient) ListLeases(ctx context.Context) ([]LeaseDescriptor,
	error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.ListLeases(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ListLeasesRequest{},
	)
	if err != nil {
		return nil, err
	}

	leases := make([]LeaseDescriptor, 0, len(resp.LockedUtxos))
	for _, leasedUtxo := range resp.LockedUtxos {
		txHash, err := chainhash.NewHash(
			leasedUtxo.Outpoint.TxidBytes,
		)
		if err != nil {
			return nil, err
		}

		if len(leasedUtxo.Id) != len(wtxmgr.LockID{}) {
			return nil, fmt.Errorf("invalid lease lock id length")
		}

		var lockID wtxmgr.LockID
		copy(lockID[:], leasedUtxo.Id)

		leases = append(leases, LeaseDescriptor{
			LockID: lockID,
			Outpoint: wire.OutPoint{
				Hash:  *txHash,
				Index: leasedUtxo.Outpoint.OutputIndex,
			},
			Value:      btcutil.Amount(leasedUtxo.Value),
			PkScript:   leasedUtxo.PkScript,
			Expiration: time.Unix(int64(leasedUtxo.Expiration), 0),
		})
	}

	return leases, nil
}

// ReleaseOutput unlocks an output, allowing it to be available for coin
// selection if it remains unspent. The ID should match the one used to
// originally lock the output.
func (m *walletKitClient) ReleaseOutput(ctx context.Context,
	lockID wtxmgr.LockID, op wire.OutPoint) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	_, err := m.client.ReleaseOutput(rpcCtx, &walletrpc.ReleaseOutputRequest{
		Id: lockID[:],
		Outpoint: &lnrpc.OutPoint{
			TxidBytes:   op.Hash[:],
			OutputIndex: op.Index,
		},
	})
	return err
}

func (m *walletKitClient) DeriveNextKey(ctx context.Context, family int32) (
	*keychain.KeyDescriptor, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.DeriveNextKey(rpcCtx, &walletrpc.KeyReq{
		KeyFamily: family,
	})
	if err != nil {
		return nil, err
	}

	key, err := btcec.ParsePubKey(resp.RawKeyBytes)
	if err != nil {
		return nil, err
	}

	return &keychain.KeyDescriptor{
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(resp.KeyLoc.KeyFamily),
			Index:  uint32(resp.KeyLoc.KeyIndex),
		},
		PubKey: key,
	}, nil
}

func (m *walletKitClient) DeriveKey(ctx context.Context, in *keychain.KeyLocator) (
	*keychain.KeyDescriptor, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.DeriveKey(rpcCtx, &signrpc.KeyLocator{
		KeyFamily: int32(in.Family),
		KeyIndex:  int32(in.Index),
	})
	if err != nil {
		return nil, err
	}

	key, err := btcec.ParsePubKey(resp.RawKeyBytes)
	if err != nil {
		return nil, err
	}

	return &keychain.KeyDescriptor{
		KeyLocator: *in,
		PubKey:     key,
	}, nil
}

func (m *walletKitClient) NextAddr(ctx context.Context, accountName string,
	addressType walletrpc.AddressType, change bool) (btcutil.Address,
	error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.NextAddr(rpcCtx, &walletrpc.AddrRequest{
		Account: accountName,
		Type:    addressType,
		Change:  change,
	})
	if err != nil {
		return nil, err
	}

	addr, err := btcutil.DecodeAddress(resp.Addr, nil)
	if err != nil {
		return nil, err
	}

	return addr, nil
}

// GetTransaction returns details for a transaction found in the wallet.
func (m *walletKitClient) GetTransaction(ctx context.Context,
	txid chainhash.Hash) (Transaction, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)

	req := &walletrpc.GetTransactionRequest{
		Txid: txid.String(),
	}
	resp, err := m.client.GetTransaction(rpcCtx, req)
	if err != nil {
		return Transaction{}, err
	}

	return unmarshallTransaction(resp)
}

func (m *walletKitClient) PublishTransaction(ctx context.Context,
	tx *wire.MsgTx, label string) error {

	txHex, err := encodeTx(tx)
	if err != nil {
		return err
	}

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	_, err = m.client.PublishTransaction(rpcCtx, &walletrpc.Transaction{
		TxHex: txHex,
		Label: label,
	})

	return err
}

func (m *walletKitClient) SendOutputs(ctx context.Context,
	outputs []*wire.TxOut, feeRate chainfee.SatPerKWeight,
	label string) (*wire.MsgTx, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.SendOutputs(rpcCtx, &walletrpc.SendOutputsRequest{
		Outputs:  marshallTxOut(outputs),
		SatPerKw: int64(feeRate),
		Label:    label,
	})
	if err != nil {
		return nil, err
	}

	tx, err := decodeTx(resp.RawTx)
	if err != nil {
		return nil, err
	}

	return tx, nil
}

func (m *walletKitClient) EstimateFeeRate(ctx context.Context, confTarget int32) (
	chainfee.SatPerKWeight, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.EstimateFee(rpcCtx, &walletrpc.EstimateFeeRequest{
		ConfTarget: confTarget,
	})
	if err != nil {
		return 0, err
	}

	return chainfee.SatPerKWeight(resp.SatPerKw), nil
}

// MinRelayFee returns the current minimum relay fee based on our chain backend
// in sat/kw.
func (m *walletKitClient) MinRelayFee(
	ctx context.Context) (chainfee.SatPerKWeight, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.EstimateFee(rpcCtx, &walletrpc.EstimateFeeRequest{
		ConfTarget: 6,
	})
	if err != nil {
		return 0, err
	}

	return chainfee.SatPerKWeight(resp.MinRelayFeeSatPerKw), nil
}

// ListSweeps returns a list of sweep transaction ids known to our node.
// Note that this function only looks up transaction ids (Verbose=false), and
// does not query our wallet for the full set of transactions.
func (m *walletKitClient) ListSweeps(ctx context.Context, startHeight int32) (
	[]string, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.ListSweeps(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ListSweepsRequest{
			Verbose:     false,
			StartHeight: startHeight,
		},
	)
	if err != nil {
		return nil, err
	}

	// Since we have requested the abbreviated response from lnd, we can
	// just get our response to a list of sweeps and return it.
	sweeps := resp.GetTransactionIds()
	return sweeps.TransactionIds, nil
}

// unmarshallOutputType translates a lnrpc.OutputScriptType into a
// txscript.ScriptClass.
func unmarshallOutputType(o lnrpc.OutputScriptType) txscript.ScriptClass {
	switch o {
	case lnrpc.OutputScriptType_SCRIPT_TYPE_SCRIPT_HASH:
		return txscript.ScriptHashTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_WITNESS_V0_PUBKEY_HASH:
		return txscript.WitnessV0PubKeyHashTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_WITNESS_V0_SCRIPT_HASH:
		return txscript.WitnessV0ScriptHashTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_PUBKEY:
		return txscript.PubKeyTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_MULTISIG:
		return txscript.MultiSigTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_NULLDATA:
		return txscript.NullDataTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_NON_STANDARD:
		return txscript.NonStandardTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_WITNESS_UNKNOWN:
		return txscript.WitnessUnknownTy

	case lnrpc.OutputScriptType_SCRIPT_TYPE_WITNESS_V1_TAPROOT:
		return txscript.WitnessV1TaprootTy

	default:
		return txscript.NonStandardTy
	}
}

// RPCTransaction returns a rpc transaction.
func UnmarshalTransactionDetail(tx *lnrpc.Transaction,
	chainParams *chaincfg.Params) (*lnwallet.TransactionDetail, error) {

	var outputDetails []lnwallet.OutputDetail
	for _, o := range tx.OutputDetails {
		address, err := btcutil.DecodeAddress(o.Address, chainParams)
		if err != nil {
			return nil, err
		}

		pkScript, err := hex.DecodeString(o.PkScript)
		if err != nil {
			return nil, err
		}

		outputDetails = append(outputDetails, lnwallet.OutputDetail{
			OutputType:   unmarshallOutputType(o.OutputType),
			Addresses:    []btcutil.Address{address},
			PkScript:     pkScript,
			OutputIndex:  int(o.OutputIndex),
			Value:        btcutil.Amount(o.Amount),
			IsOurAddress: o.IsOurAddress,
		})
	}

	previousOutpoints := make(
		[]lnwallet.PreviousOutPoint, len(tx.PreviousOutpoints),
	)
	for idx, previousOutPoint := range tx.PreviousOutpoints {
		previousOutpoints[idx] = lnwallet.PreviousOutPoint{
			OutPoint:    previousOutPoint.Outpoint,
			IsOurOutput: previousOutPoint.IsOurOutput,
		}
	}

	// We also get unconfirmed transactions, so BlockHash can be empty.
	var (
		blockHash *chainhash.Hash
		err       error
	)

	if tx.BlockHash != "" {
		blockHash, err = chainhash.NewHashFromStr(tx.BlockHash)
		if err != nil {
			return nil, err
		}
	}

	txHash, err := chainhash.NewHashFromStr(tx.TxHash)
	if err != nil {
		return nil, err
	}

	rawTx, err := hex.DecodeString(tx.RawTxHex)
	if err != nil {
		return nil, err
	}

	return &lnwallet.TransactionDetail{
		Hash:              *txHash,
		Value:             btcutil.Amount(tx.Amount),
		NumConfirmations:  tx.NumConfirmations,
		BlockHash:         blockHash,
		BlockHeight:       tx.BlockHeight,
		Timestamp:         tx.TimeStamp,
		TotalFees:         tx.TotalFees,
		OutputDetails:     outputDetails,
		RawTx:             rawTx,
		Label:             tx.Label,
		PreviousOutpoints: previousOutpoints,
	}, nil
}

// ListSweepsVerbose returns a list of sweep transactions known to our node
// with verbose information about each sweep.
func (m *walletKitClient) ListSweepsVerbose(ctx context.Context,
	startHeight int32) ([]lnwallet.TransactionDetail, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.ListSweeps(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ListSweepsRequest{
			Verbose:     true,
			StartHeight: startHeight,
		},
	)
	if err != nil {
		return nil, err
	}

	// Since we have requested the verbose response from LND, we need to
	// unmarshal transaction details for each individual sweep.
	rpcDetails := resp.GetTransactionDetails()
	if rpcDetails == nil {
		return nil, fmt.Errorf("invalid transaction details")
	}

	var result []lnwallet.TransactionDetail
	for _, txDetail := range rpcDetails.Transactions {
		tx, err := UnmarshalTransactionDetail(txDetail, m.params)
		if err != nil {
			return nil, err
		}
		result = append(result, *tx)
	}

	return result, nil
}

// BumpFeeOption customizes a BumpFee call.
type BumpFeeOption func(*walletrpc.BumpFeeRequest)

// WithImmediate is an option for enabling the immediate mode of BumpFee. The
// sweeper will sweep this input without waiting for the next block.
func WithImmediate() BumpFeeOption {
	return func(r *walletrpc.BumpFeeRequest) {
		r.Immediate = true
	}
}

// WithTargetConf is an option for setting the target_conf of BumpFee. If set,
// the underlying fee estimator will use the target_conf to estimate the
// starting fee rate for the fee function. Pass feeRate=0 to BumpFee if you
// add this option.
func WithTargetConf(targetConf uint32) BumpFeeOption {
	return func(r *walletrpc.BumpFeeRequest) {
		r.TargetConf = targetConf
	}
}

// WithBudget is an option for setting the budget of BumpFee. It is the max
// amount in sats that can be used as the fees. Setting this value greater than
// the input's value may result in CPFP - one or more wallet utxos will be used
// to pay the fees specified by the budget. If not set, for new inputs, by
// default 50% of the input's value will be treated as the budget for fee
// bumping; for existing inputs, their current budgets will be retained.
func WithBudget(budget btcutil.Amount) BumpFeeOption {
	return func(r *walletrpc.BumpFeeRequest) {
		r.Budget = uint64(budget)
	}
}

// BumpFee attempts to bump the fee of a transaction by spending one of its
// outputs at the given fee rate. This essentially results in a
// child-pays-for-parent (CPFP) scenario. If the given output has been used in a
// previous BumpFee call, then a transaction replacing the previous is
// broadcast, resulting in a replace-by-fee (RBF) scenario.
func (m *walletKitClient) BumpFee(ctx context.Context, op wire.OutPoint,
	feeRate chainfee.SatPerKWeight, opts ...BumpFeeOption) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	req := &walletrpc.BumpFeeRequest{
		Outpoint: &lnrpc.OutPoint{
			TxidBytes:   op.Hash[:],
			OutputIndex: op.Index,
		},
		SatPerVbyte: uint64(feeRate.FeePerVByte()),
		Immediate:   false,
	}

	for _, opt := range opts {
		opt(req)
	}

	// Make sure that feeRate and WithTargetConf are not used together.
	if feeRate != 0 && req.TargetConf != 0 {
		return fmt.Errorf("can't use target_conf if feeRate != 0")
	}

	_, err := m.client.BumpFee(m.walletKitMac.WithMacaroonAuth(rpcCtx), req)

	return err
}

// ListAccounts retrieves all accounts belonging to the wallet by default.
// Optional name and addressType can be provided to filter through all of the
// wallet accounts and return only those matching.
func (m *walletKitClient) ListAccounts(ctx context.Context, name string,
	addressType walletrpc.AddressType) ([]*walletrpc.Account, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.ListAccounts(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ListAccountsRequest{
			Name:        name,
			AddressType: addressType,
		},
	)
	if err != nil {
		return nil, err
	}

	return resp.GetAccounts(), nil
}

// FundPsbt creates a fully populated PSBT that contains enough inputs
// to fund the outputs specified in the template. There are two ways of
// specifying a template: Either by passing in a PSBT with at least one
// output declared or by passing in a raw TxTemplate message. If there
// are no inputs specified in the template, coin selection is performed
// automatically. If the template does contain any inputs, it is assumed
// that full coin selection happened externally and no additional inputs
// are added. If the specified inputs aren't enough to fund the outputs
// with the given fee rate, an error is returned.
// After either selecting or verifying the inputs, all input UTXOs are
// locked with an internal app ID.
//
// NOTE: If this method returns without an error, it is the caller's
// responsibility to either spend the locked UTXOs (by finalizing and
// then publishing the transaction) or to unlock/release the locked
// UTXOs in case of an error on the caller's side.
func (m *walletKitClient) FundPsbt(ctx context.Context,
	req *walletrpc.FundPsbtRequest) (*psbt.Packet, int32,
	[]*walletrpc.UtxoLease, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.FundPsbt(
		m.walletKitMac.WithMacaroonAuth(rpcCtx), req,
	)
	if err != nil {
		return nil, 0, nil, err
	}

	packet, err := psbt.NewFromRawBytes(
		bytes.NewReader(resp.FundedPsbt), false,
	)
	if err != nil {
		return nil, 0, nil, err
	}

	return packet, resp.ChangeOutputIndex, resp.LockedUtxos, nil
}

// SignPsbt expects a partial transaction with all inputs and outputs
// fully declared and tries to sign all unsigned inputs that have all
// required fields (UTXO information, BIP32 derivation information,
// witness or sig scripts) set.
// If no error is returned, the PSBT is ready to be given to the next
// signer or to be finalized if lnd was the last signer.
//
// NOTE: This RPC only signs inputs (and only those it can sign), it
// does not perform any other tasks (such as coin selection, UTXO
// locking or input/output/fee value validation, PSBT finalization). Any
// input that is incomplete will be skipped.
func (m *walletKitClient) SignPsbt(ctx context.Context,
	packet *psbt.Packet) (*psbt.Packet, error) {

	var psbtBuf bytes.Buffer
	if err := packet.Serialize(&psbtBuf); err != nil {
		return nil, err
	}

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.SignPsbt(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.SignPsbtRequest{FundedPsbt: psbtBuf.Bytes()},
	)
	if err != nil {
		return nil, err
	}

	signedPacket, err := psbt.NewFromRawBytes(
		bytes.NewReader(resp.SignedPsbt), false,
	)
	if err != nil {
		return nil, err
	}

	return signedPacket, nil
}

// FinalizePsbt expects a partial transaction with all inputs and
// outputs fully declared and tries to sign all inputs that belong to
// the wallet. Lnd must be the last signer of the transaction. That
// means, if there are any unsigned non-witness inputs or inputs without
// UTXO information attached or inputs without witness data that do not
// belong to lnd's wallet, this method will fail. If no error is
// returned, the PSBT is ready to be extracted and the final TX within
// to be broadcast.
//
// NOTE: This method does NOT publish the transaction once finalized. It
// is the caller's responsibility to either publish the transaction on
// success or unlock/release any locked UTXOs in case of an error in
// this method.
func (m *walletKitClient) FinalizePsbt(ctx context.Context, packet *psbt.Packet,
	account string) (*psbt.Packet, *wire.MsgTx, error) {

	var psbtBuf bytes.Buffer
	if err := packet.Serialize(&psbtBuf); err != nil {
		return nil, nil, err
	}

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.FinalizePsbt(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.FinalizePsbtRequest{
			FundedPsbt: psbtBuf.Bytes(),
			Account:    account,
		},
	)
	if err != nil {
		return nil, nil, err
	}

	finalizedPacket, err := psbt.NewFromRawBytes(
		bytes.NewReader(resp.SignedPsbt), false,
	)
	if err != nil {
		return nil, nil, err
	}

	finalTx := wire.NewMsgTx(2)
	err = finalTx.Deserialize(bytes.NewReader(resp.RawFinalTx))
	if err != nil {
		return nil, nil, err
	}

	return finalizedPacket, finalTx, nil
}

// ImportPublicKey imports a public key as watch-only into the wallet.
//
// NOTE: Events (deposits/spends) for a key will only be detected by lnd if they
// happen after the import. Rescans to detect past events will be supported
// later on.
func (m *walletKitClient) ImportPublicKey(ctx context.Context,
	pubKey *btcec.PublicKey, addrType lnwallet.AddressType) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	var rpcAddrType walletrpc.AddressType
	switch addrType {
	case lnwallet.WitnessPubKey:
		rpcAddrType = walletrpc.AddressType_WITNESS_PUBKEY_HASH
	case lnwallet.NestedWitnessPubKey:
		rpcAddrType = walletrpc.AddressType_NESTED_WITNESS_PUBKEY_HASH
	case lnwallet.TaprootPubkey:
		rpcAddrType = walletrpc.AddressType_TAPROOT_PUBKEY
	default:
		return fmt.Errorf("invalid utxo address type %v", addrType)
	}

	_, err := m.client.ImportPublicKey(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ImportPublicKeyRequest{
			PublicKey:   pubKey.SerializeCompressed(),
			AddressType: rpcAddrType,
		},
	)
	return err
}

// ImportTaprootScript imports a user-provided taproot script into the wallet.
// The imported script will act as a pay-to-taproot address.
//
// NOTE: Events (deposits/spends) for a key will only be detected by lnd if they
// happen after the import. Rescans to detect past events will be supported
// later on.
//
// NOTE: Taproot keys imported through this RPC currently _cannot_ be used for
// funding PSBTs. Only tracking the balance and UTXOs is currently supported.
func (m *walletKitClient) ImportTaprootScript(ctx context.Context,
	tapscript *waddrmgr.Tapscript) (btcutil.Address, error) {

	if tapscript == nil {
		return nil, fmt.Errorf("invalid tapscript")
	}

	var (
		rpcReq    = &walletrpc.ImportTapscriptRequest{}
		ctrlBlock = tapscript.ControlBlock
	)

	switch tapscript.Type {
	case waddrmgr.TapscriptTypeFullTree:
		rpcReq.InternalPublicKey = schnorr.SerializePubKey(
			ctrlBlock.InternalKey,
		)

		rpcLeaves := make([]*walletrpc.TapLeaf, len(tapscript.Leaves))
		for idx, leaf := range tapscript.Leaves {
			rpcLeaves[idx] = &walletrpc.TapLeaf{
				LeafVersion: uint32(leaf.LeafVersion),
				Script:      leaf.Script,
			}
		}
		rpcReq.Script = &walletrpc.ImportTapscriptRequest_FullTree{
			FullTree: &walletrpc.TapscriptFullTree{
				AllLeaves: rpcLeaves,
			},
		}

	case waddrmgr.TapscriptTypePartialReveal:
		rpcReq.InternalPublicKey = schnorr.SerializePubKey(
			ctrlBlock.InternalKey,
		)
		rpcReq.Script = &walletrpc.ImportTapscriptRequest_PartialReveal{
			PartialReveal: &walletrpc.TapscriptPartialReveal{
				RevealedLeaf: &walletrpc.TapLeaf{
					LeafVersion: uint32(
						ctrlBlock.LeafVersion,
					),
					Script: tapscript.RevealedScript,
				},
				FullInclusionProof: ctrlBlock.InclusionProof,
			},
		}

	case waddrmgr.TaprootKeySpendRootHash:
		rpcReq.InternalPublicKey = schnorr.SerializePubKey(
			ctrlBlock.InternalKey,
		)
		rpcReq.Script = &walletrpc.ImportTapscriptRequest_RootHashOnly{
			RootHashOnly: tapscript.RootHash,
		}

	case waddrmgr.TaprootFullKeyOnly:
		rpcReq.InternalPublicKey = schnorr.SerializePubKey(
			tapscript.FullOutputKey,
		)
		rpcReq.Script = &walletrpc.ImportTapscriptRequest_FullKeyOnly{
			FullKeyOnly: true,
		}

	default:
		return nil, fmt.Errorf("invalid tapscript type <%d>",
			tapscript.Type)
	}

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	importResp, err := m.client.ImportTapscript(
		m.walletKitMac.WithMacaroonAuth(rpcCtx), rpcReq,
	)
	if err != nil {
		return nil, fmt.Errorf("error importing tapscript into lnd: %v",
			err)
	}

	p2trAddr, err := btcutil.DecodeAddress(importResp.P2TrAddress, m.params)
	if err != nil {
		return nil, fmt.Errorf("error parsing imported p2tr addr: %v",
			err)
	}

	return p2trAddr, nil
}
