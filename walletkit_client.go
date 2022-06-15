package lndclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/wtxmgr"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
	"google.golang.org/grpc"
)

// WalletKitClient exposes wallet functionality.
type WalletKitClient interface {
	// ListUnspent returns a list of all utxos spendable by the wallet with
	// a number of confirmations between the specified minimum and maximum.
	ListUnspent(ctx context.Context, minConfs, maxConfs int32) (
		[]*lnwallet.Utxo, error)

	// LeaseOutput locks an output to the given ID for the lease time
	// provided, preventing it from being available for any future coin
	// selection attempts. The absolute time of the lock's expiration is
	// returned. The expiration of the lock can be extended by successive
	// invocations of this call. Outputs can be unlocked before their
	// expiration through `ReleaseOutput`.
	LeaseOutput(ctx context.Context, lockID wtxmgr.LockID,
		op wire.OutPoint, leaseTime time.Duration) (time.Time, error)

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

	PublishTransaction(ctx context.Context, tx *wire.MsgTx,
		label string) error

	SendOutputs(ctx context.Context, outputs []*wire.TxOut,
		feeRate chainfee.SatPerKWeight,
		label string) (*wire.MsgTx, error)

	EstimateFeeRate(ctx context.Context,
		confTarget int32) (chainfee.SatPerKWeight, error)

	// ListSweeps returns a list of sweep transaction ids known to our node.
	// Note that this function only looks up transaction ids, and does not
	// query our wallet for the full set of transactions.
	ListSweeps(ctx context.Context) ([]string, error)

	// BumpFee attempts to bump the fee of a transaction by spending one of
	// its outputs at the given fee rate. This essentially results in a
	// child-pays-for-parent (CPFP) scenario. If the given output has been
	// used in a previous BumpFee call, then a transaction replacing the
	// previous is broadcast, resulting in a replace-by-fee (RBF) scenario.
	BumpFee(context.Context, wire.OutPoint, chainfee.SatPerKWeight) error

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
	ImportPublicKey(ctx context.Context, pubkey *btcec.PublicKey,
		addrType lnwallet.AddressType) error
}

type walletKitClient struct {
	client       walletrpc.WalletKitClient
	walletKitMac serializedMacaroon
	timeout      time.Duration
}

// A compile-time constraint to ensure walletKitclient satisfies the
// WalletKitClient interface.
var _ WalletKitClient = (*walletKitClient)(nil)

func newWalletKitClient(conn grpc.ClientConnInterface,
	walletKitMac serializedMacaroon, timeout time.Duration) *walletKitClient {

	return &walletKitClient{
		client:       walletrpc.NewWalletKitClient(conn),
		walletKitMac: walletKitMac,
		timeout:      timeout,
	}
}

// ListUnspent returns a list of all utxos spendable by the wallet with a number
// of confirmations between the specified minimum and maximum.
func (m *walletKitClient) ListUnspent(ctx context.Context, minConfs,
	maxConfs int32) ([]*lnwallet.Utxo, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.walletKitMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.ListUnspent(rpcCtx, &walletrpc.ListUnspentRequest{
		MinConfs: minConfs,
		MaxConfs: maxConfs,
	})
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

// ListSweeps returns a list of sweep transaction ids known to our node.
// Note that this function only looks up transaction ids (Verbose=false), and
// does not query our wallet for the full set of transactions.
func (m *walletKitClient) ListSweeps(ctx context.Context) ([]string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	resp, err := m.client.ListSweeps(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.ListSweepsRequest{
			Verbose: false,
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

// BumpFee attempts to bump the fee of a transaction by spending one of its
// outputs at the given fee rate. This essentially results in a
// child-pays-for-parent (CPFP) scenario. If the given output has been used in a
// previous BumpFee call, then a transaction replacing the previous is
// broadcast, resulting in a replace-by-fee (RBF) scenario.
func (m *walletKitClient) BumpFee(ctx context.Context, op wire.OutPoint,
	feeRate chainfee.SatPerKWeight) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	_, err := m.client.BumpFee(
		m.walletKitMac.WithMacaroonAuth(rpcCtx),
		&walletrpc.BumpFeeRequest{
			Outpoint: &lnrpc.OutPoint{
				TxidBytes:   op.Hash[:],
				OutputIndex: op.Index,
			},
			SatPerByte: uint32(feeRate.FeePerKVByte() / 1000),
			Force:      false,
		},
	)
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
