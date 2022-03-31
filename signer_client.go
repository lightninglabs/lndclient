package lndclient

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr/musig2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"google.golang.org/grpc"
)

// SignerClient exposes sign functionality.
type SignerClient interface {
	// SignOutputRaw is a method that can be used to generate a signature
	// for a set of inputs/outputs to a transaction. Each request specifies
	// details concerning how the outputs should be signed, which keys they
	// should be signed with, and also any optional tweaks.
	SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
		signDescriptors []*SignDescriptor,
		prevOutputs []*wire.TxOut) ([][]byte, error)

	// ComputeInputScript generates the proper input script for P2WPKH
	// output and NP2WPKH outputs. This method only requires that the
	// `Output`, `HashType`, `SigHashes` and `InputIndex` fields are
	// populated within the sign descriptors.
	ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
		signDescriptors []*SignDescriptor) ([]*input.Script, error)

	// SignMessage signs a message with the key specified in the key
	// locator. The returned signature is fixed-size LN wire format encoded.
	SignMessage(ctx context.Context, msg []byte,
		locator keychain.KeyLocator) ([]byte, error)

	// VerifyMessage verifies a signature over a message using the public
	// key provided. The signature must be fixed-size LN wire format
	// encoded.
	VerifyMessage(ctx context.Context, msg, sig []byte, pubkey [33]byte) (
		bool, error)

	// DeriveSharedKey returns a shared secret key by performing
	// Diffie-Hellman key derivation between the ephemeral public key and
	// the key specified by the key locator (or the node's identity private
	// key if no key locator is specified):
	//
	//     P_shared = privKeyNode * ephemeralPubkey
	//
	// The resulting shared public key is serialized in the compressed
	// format and hashed with SHA256, resulting in a final key length of 256
	// bits.
	DeriveSharedKey(ctx context.Context, ephemeralPubKey *btcec.PublicKey,
		keyLocator *keychain.KeyLocator) ([32]byte, error)

	// NewMuSig2Session creates a new musig session with the key and
	// signers provided.
	NewMuSig2Session(ctx context.Context,
		signerLoc *keychain.KeyLocator, signers [][32]byte,
		opts ...MuSigSessionOpts) (*MuSig2Session, error)

	// MuSig2RegisterNonces registers additional public nonces for a musig2
	// session. It returns a boolean indicating whether we have all of our
	// nonces present.
	MuSig2RegisterNonces(ctx context.Context, sessionID [99]byte,
		nonces [][musig2.PubNonceSize]byte) (bool, error)

	// MuSig2Sign creates a partial signature for the 32 byte SHA256 digest
	// of a message. This can only be called once all public nonces have
	// been created. If the caller will not be responsible for combining
	// the signatures, the cleanup bool should be set.
	MuSig2Sign(ctx context.Context, sessionID [99]byte,
		message [32]byte, cleanup bool) ([]byte, error)

	// MuSig2CombineSig combines the given partial signature(s) with the
	// local one, if it already exists. Once a partial signature of all
	// participants are registered, the final signature will be combined
	// and returned.
	MuSig2Combine(ctx context.Context, sessionID [99]byte,
		otherPartialSigs [][]byte) (bool, []byte, error)
}

// SignDescriptor houses the necessary information required to successfully
// sign a given segwit output. This struct is used by the Signer interface in
// order to gain access to critical data needed to generate a valid signature.
type SignDescriptor struct {
	// KeyDesc is a descriptor that precisely describes *which* key to use
	// for signing. This may provide the raw public key directly, or
	// require the Signer to re-derive the key according to the populated
	// derivation path.
	KeyDesc keychain.KeyDescriptor

	// SingleTweak is a scalar value that will be added to the private key
	// corresponding to the above public key to obtain the private key to
	// be used to sign this input. This value is typically derived via the
	// following computation:
	//
	//  * derivedKey = privkey + sha256(perCommitmentPoint || pubKey) mod N
	//
	// NOTE: If this value is nil, then the input can be signed using only
	// the above public key. Either a SingleTweak should be set or a
	// DoubleTweak, not both.
	SingleTweak []byte

	// DoubleTweak is a private key that will be used in combination with
	// its corresponding private key to derive the private key that is to
	// be used to sign the target input. Within the Lightning protocol,
	// this value is typically the commitment secret from a previously
	// revoked commitment transaction. This value is in combination with
	// two hash values, and the original private key to derive the private
	// key to be used when signing.
	//
	//  * k = (privKey*sha256(pubKey || tweakPub) +
	//        tweakPriv*sha256(tweakPub || pubKey)) mod N
	//
	// NOTE: If this value is nil, then the input can be signed using only
	// the above public key. Either a SingleTweak should be set or a
	// DoubleTweak, not both.
	DoubleTweak *btcec.PrivateKey

	// WitnessScript is the full script required to properly redeem the
	// output. This field should be set to the full script if a p2wsh
	// output is being signed. For p2wkh it should be set to the hashed
	// script (PkScript).
	WitnessScript []byte

	// TaprootKeySpend indicates that instead of a witness script being
	// spent by the signature that results from this signing request, a
	// taproot key spend is performed instead.
	TaprootKeySpend bool

	// Output is the target output which should be signed. The PkScript and
	// Value fields within the output should be properly populated,
	// otherwise an invalid signature may be generated.
	Output *wire.TxOut

	// HashType is the target sighash type that should be used when
	// generating the final sighash, and signature.
	HashType txscript.SigHashType

	// InputIndex is the target input within the transaction that should be
	// signed.
	InputIndex int
}

type signerClient struct {
	client    signrpc.SignerClient
	signerMac serializedMacaroon
	timeout   time.Duration
}

func newSignerClient(conn grpc.ClientConnInterface,
	signerMac serializedMacaroon, timeout time.Duration) *signerClient {

	return &signerClient{
		client:    signrpc.NewSignerClient(conn),
		signerMac: signerMac,
		timeout:   timeout,
	}
}

func marshallSignDescriptors(signDescriptors []*SignDescriptor,
) []*signrpc.SignDescriptor {

	rpcSignDescs := make([]*signrpc.SignDescriptor, len(signDescriptors))
	for i, signDesc := range signDescriptors {
		var keyBytes []byte
		var keyLocator *signrpc.KeyLocator
		if signDesc.KeyDesc.PubKey != nil {
			keyBytes = signDesc.KeyDesc.PubKey.SerializeCompressed()
		} else {
			keyLocator = &signrpc.KeyLocator{
				KeyFamily: int32(
					signDesc.KeyDesc.KeyLocator.Family,
				),
				KeyIndex: int32(
					signDesc.KeyDesc.KeyLocator.Index,
				),
			}
		}

		var doubleTweak []byte
		if signDesc.DoubleTweak != nil {
			doubleTweak = signDesc.DoubleTweak.Serialize()
		}

		rpcSignDescs[i] = &signrpc.SignDescriptor{
			WitnessScript:   signDesc.WitnessScript,
			TaprootKeySpend: signDesc.TaprootKeySpend,
			Output: &signrpc.TxOut{
				PkScript: signDesc.Output.PkScript,
				Value:    signDesc.Output.Value,
			},
			Sighash:    uint32(signDesc.HashType),
			InputIndex: int32(signDesc.InputIndex),
			KeyDesc: &signrpc.KeyDescriptor{
				RawKeyBytes: keyBytes,
				KeyLoc:      keyLocator,
			},
			SingleTweak: signDesc.SingleTweak,
			DoubleTweak: doubleTweak,
		}
	}

	return rpcSignDescs
}

// marshallTxOut marshals the transaction outputs as their RPC counterparts.
func marshallTxOut(outputs []*wire.TxOut) []*signrpc.TxOut {
	rpcOutputs := make([]*signrpc.TxOut, len(outputs))
	for i, output := range outputs {
		rpcOutputs[i] = &signrpc.TxOut{
			PkScript: output.PkScript,
			Value:    output.Value,
		}
	}

	return rpcOutputs
}

// SignOutputRaw is a method that can be used to generate a signature for a set
// of inputs/outputs to a transaction. Each request specifies details concerning
// how the outputs should be signed, which keys they should be signed with, and
// also any optional tweaks.
func (s *signerClient) SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*SignDescriptor, prevOutputs []*wire.TxOut) ([][]byte,
	error) {

	txRaw, err := encodeTx(tx)
	if err != nil {
		return nil, err
	}
	rpcSignDescs := marshallSignDescriptors(signDescriptors)
	rpcPervOutputs := marshallTxOut(prevOutputs)

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.SignOutputRaw(rpcCtx,
		&signrpc.SignReq{
			RawTxBytes:  txRaw,
			SignDescs:   rpcSignDescs,
			PrevOutputs: rpcPervOutputs,
		},
	)
	if err != nil {
		return nil, err
	}

	return resp.RawSigs, nil
}

// ComputeInputScript generates the proper input script for P2WPKH output and
// NP2WPKH outputs. This method only requires that the `Output`, `HashType`,
// `SigHashes` and `InputIndex` fields are populated within the sign
// descriptors.
func (s *signerClient) ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*SignDescriptor) ([]*input.Script, error) {

	txRaw, err := encodeTx(tx)
	if err != nil {
		return nil, err
	}
	rpcSignDescs := marshallSignDescriptors(signDescriptors)

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.ComputeInputScript(
		rpcCtx, &signrpc.SignReq{
			RawTxBytes: txRaw,
			SignDescs:  rpcSignDescs,
		},
	)
	if err != nil {
		return nil, err
	}

	inputScripts := make([]*input.Script, 0, len(resp.InputScripts))
	for _, inputScript := range resp.InputScripts {
		inputScripts = append(inputScripts, &input.Script{
			SigScript: inputScript.SigScript,
			Witness:   inputScript.Witness,
		})
	}

	return inputScripts, nil
}

// SignMessage signs a message with the key specified in the key locator. The
// returned signature is fixed-size LN wire format encoded.
func (s *signerClient) SignMessage(ctx context.Context, msg []byte,
	locator keychain.KeyLocator) ([]byte, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcIn := &signrpc.SignMessageReq{
		Msg: msg,
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(locator.Family),
			KeyIndex:  int32(locator.Index),
		},
	}

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.SignMessage(rpcCtx, rpcIn)
	if err != nil {
		return nil, err
	}

	return resp.Signature, nil
}

// VerifyMessage verifies a signature over a message using the public key
// provided. The signature must be fixed-size LN wire format encoded.
func (s *signerClient) VerifyMessage(ctx context.Context, msg, sig []byte,
	pubkey [33]byte) (bool, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcIn := &signrpc.VerifyMessageReq{
		Msg:       msg,
		Signature: sig,
		Pubkey:    pubkey[:],
	}

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.VerifyMessage(rpcCtx, rpcIn)
	if err != nil {
		return false, err
	}
	return resp.Valid, nil
}

// DeriveSharedKey returns a shared secret key by performing Diffie-Hellman key
// derivation between the ephemeral public key and the key specified by the key
// locator (or the node's identity private key if no key locator is specified):
//
//     P_shared = privKeyNode * ephemeralPubkey
//
// The resulting shared public key is serialized in the compressed format and
// hashed with SHA256, resulting in a final key length of 256 bits.
func (s *signerClient) DeriveSharedKey(ctx context.Context,
	ephemeralPubKey *btcec.PublicKey,
	keyLocator *keychain.KeyLocator) ([32]byte, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcIn := &signrpc.SharedKeyRequest{
		EphemeralPubkey: ephemeralPubKey.SerializeCompressed(),
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(keyLocator.Family),
			KeyIndex:  int32(keyLocator.Index),
		},
	}

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.DeriveSharedKey(rpcCtx, rpcIn)
	if err != nil {
		return [32]byte{}, err
	}

	var sharedKey [32]byte
	copy(sharedKey[:], resp.SharedKey)
	return sharedKey, nil
}

// MuSigSessionOpts is the signature used to apply functional options to
// musig session requests.
type MuSigSessionOpts func(*signrpc.MuSig2SessionRequest)

// noncesToBytes converts a set of public nonces to a [][]byte.
func noncesToBytes(nonces [][musig2.PubNonceSize]byte) [][]byte {
	nonceBytes := make([][]byte, len(nonces))

	for i, nonce := range nonces {
		nonceBytes[i] = nonce[:]
	}

	return nonceBytes
}

// MusigNonceOpt adds an optional set of nonces to a musig session request.
func MusigNonceOpt(nonces [][musig2.PubNonceSize]byte) MuSigSessionOpts {
	return func(s *signrpc.MuSig2SessionRequest) {
		s.OtherSignerPublicNonces = noncesToBytes(nonces)
	}
}

// MuSig2Session contains session information.
type MuSig2Session struct {
	// SessionID identifies the session.
	SessionID [99]byte

	// CombinedKey is the combined public key of all signers.
	CombinedKey *btcec.PublicKey

	// LocalPublicNonces are our two public nonces.
	LocalPublicNonces [musig2.PubNonceSize]byte

	// HaveAllNonces indicates whether all nonces required to start the
	// signing process are known now.
	HaveAllNonces bool
}

// NewMuSig2Session creates a new musig session with the key and signers
// provided.
func (s *signerClient) NewMuSig2Session(ctx context.Context,
	signerLoc *keychain.KeyLocator, signers [][32]byte,
	opts ...MuSigSessionOpts) (*MuSig2Session, error) {

	signerBytes := make([][]byte, len(signers))
	for i, signer := range signers {
		signerBytes[i] = make([]byte, 32)
		copy(signerBytes[i], signer[:])
	}

	req := &signrpc.MuSig2SessionRequest{
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(signerLoc.Family),
			KeyIndex:  int32(signerLoc.Index),
		},
		AllSignerPubkeys: signerBytes,
	}

	for _, opt := range opts {
		opt(req)
	}

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.MuSig2CreateSession(rpcCtx, req)
	if err != nil {
		return nil, err
	}

	combinedKey, err := schnorr.ParsePubKey(resp.CombinedKey)
	if err != nil {
		return nil, fmt.Errorf("could not parse combined key: %v", err)
	}

	session := &MuSig2Session{
		CombinedKey:   combinedKey,
		HaveAllNonces: resp.HaveAllNonces,
	}

	if len(resp.LocalPublicNonces) != musig2.PubNonceSize {
		return nil, fmt.Errorf("unexpected local nonce size: %v",
			len(resp.LocalPublicNonces))
	}
	copy(session.LocalPublicNonces[:], resp.LocalPublicNonces)

	// TODO: repalce with signrpc.MuSig2SessionIDSize
	if len(resp.SessionId) != 99 {
		return nil, fmt.Errorf("unexpected session ID length: %v",
			len(resp.SessionId))
	}

	copy(session.SessionID[:], resp.SessionId)

	return session, nil
}

// MuSig2RegisterNonces registers additional public nonces for a musig2 session.
// It returns a boolean indicating whether we have all of our nonces present.
func (s *signerClient) MuSig2RegisterNonces(ctx context.Context,
	sessionID [99]byte, nonces [][musig2.PubNonceSize]byte) (bool, error) {

	req := &signrpc.MuSig2RegisterNoncesRequest{
		SessionId:               sessionID[:],
		OtherSignerPublicNonces: noncesToBytes(nonces),
	}

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.MuSig2RegisterNonces(rpcCtx, req)
	if err != nil {
		return false, err
	}

	return resp.HaveAllNonces, nil
}

// MuSig2Sign creates a partial signature for the 32 byte SHA256 digest of a
// message. This can only be called once all public nonces have been created.
// If the caller will not be responsible for combining the signatures, the
// cleanup bool should be set.
func (s *signerClient) MuSig2Sign(ctx context.Context, sessionID [99]byte,
	message [32]byte, cleanup bool) ([]byte, error) {

	req := &signrpc.MuSig2SignRequest{
		SessionId:     sessionID[:],
		MessageDigest: message[:],
		Cleanup:       cleanup,
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.MuSig2Sign(rpcCtx, req)
	if err != nil {
		return nil, err
	}

	return resp.LocalPartialSignature, nil
}

// MuSig2CombineSig combines the given partial signature(s) with the local one,
// if it already exists. Once a partial signature of all participants are
// registered, the final signature will be combined and returned.
func (s *signerClient) MuSig2Combine(ctx context.Context, sessionID [99]byte,
	otherPartialSigs [][]byte) (bool, []byte, error) {

	req := &signrpc.MuSig2CombineSigRequest{
		SessionId:              sessionID[:],
		OtherPartialSignatures: otherPartialSigs,
	}

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.MuSig2CombineSig(rpcCtx, req)
	if err != nil {
		return false, nil, err
	}

	return resp.HaveAllSignatures, resp.FinalSignature, nil
}
