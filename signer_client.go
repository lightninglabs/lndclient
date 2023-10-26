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
		signDescriptors []*SignDescriptor, prevOutputs []*wire.TxOut) (
		[]*input.Script, error)

	// SignMessage signs a message with the key specified in the key
	// locator. The returned signature is fixed-size LN wire format encoded.
	SignMessage(ctx context.Context, msg []byte,
		locator keychain.KeyLocator, opts ...SignMessageOption) ([]byte,
		error)

	// VerifyMessage verifies a signature over a message using the public
	// key provided. The signature must be fixed-size LN wire format
	// encoded.
	VerifyMessage(ctx context.Context, msg, sig []byte, pubkey [33]byte,
		opts ...VerifyMessageOption) (bool, error)

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

	// MuSig2CreateSession creates a new musig session with the key and
	// signers provided. Note that depending on the version the signer keys
	// may need to be either 33 byte public keys or 32 byte Schnorr public
	// keys.
	MuSig2CreateSession(ctx context.Context, version input.MuSig2Version,
		signerLoc *keychain.KeyLocator, signers [][]byte,
		opts ...MuSig2SessionOpts) (*input.MuSig2SessionInfo, error)

	// MuSig2RegisterNonces registers additional public nonces for a musig2
	// session. It returns a boolean indicating whether we have all of our
	// nonces present.
	MuSig2RegisterNonces(ctx context.Context, sessionID [32]byte,
		nonces [][66]byte) (bool, error)

	// MuSig2Sign creates a partial signature for the 32 byte SHA256 digest
	// of a message. This can only be called once all public nonces have
	// been created. If the caller will not be responsible for combining
	// the signatures, the cleanup bool should be set.
	MuSig2Sign(ctx context.Context, sessionID [32]byte,
		message [32]byte, cleanup bool) ([]byte, error)

	// MuSig2CombineSig combines the given partial signature(s) with the
	// local one, if it already exists. Once a partial signature of all
	// participants are registered, the final signature will be combined
	// and returned.
	MuSig2CombineSig(ctx context.Context, sessionID [32]byte,
		otherPartialSigs [][]byte) (bool, []byte, error)

	// MuSig2Cleanup removes a session from memory to free up resources.
	MuSig2Cleanup(ctx context.Context, sessionID [32]byte) error
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

	// The 32 byte input to the taproot tweak derivation that is used to
	// derive the output key from an internal key: outputKey = internalKey +
	// tagged_hash("tapTweak", internalKey || tapTweak).
	//
	// When doing a BIP 86 spend, this field can be an empty byte slice.
	//
	// When doing a normal key path spend, with the output key committing to
	// an actual script root, then this field should be: the tapscript root
	// hash.
	TapTweak []byte

	// WitnessScript is the full script required to properly redeem the
	// output. This field should be set to the full script if a p2wsh or
	// p2tr output is being signed. For p2wkh it should be set to the hashed
	// script (PkScript), for p2tr this should be the raw leaf script that's
	// being spent.
	WitnessScript []byte

	// TaprootKeySpend specifies how the input should be signed. Depending
	// on the method, either the tap_tweak, witness_script or both need to
	// be specified. Defaults to SegWit v0 signing to be backward compatible
	// with older RPC clients.
	SignMethod input.SignMethod

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

// MarshalSignMethod turns the native sign method into the RPC counterpart.
func MarshalSignMethod(signMethod input.SignMethod) signrpc.SignMethod {
	switch signMethod {
	case input.TaprootKeySpendBIP0086SignMethod:
		return signrpc.SignMethod_SIGN_METHOD_TAPROOT_KEY_SPEND_BIP0086

	case input.TaprootKeySpendSignMethod:
		return signrpc.SignMethod_SIGN_METHOD_TAPROOT_KEY_SPEND

	case input.TaprootScriptSpendSignMethod:
		return signrpc.SignMethod_SIGN_METHOD_TAPROOT_SCRIPT_SPEND

	default:
		return signrpc.SignMethod_SIGN_METHOD_WITNESS_V0
	}
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

func marshallSignDescriptors(
	signDescriptors []*SignDescriptor) []*signrpc.SignDescriptor {

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
			WitnessScript: signDesc.WitnessScript,
			SignMethod:    MarshalSignMethod(signDesc.SignMethod),
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
			TapTweak:    signDesc.TapTweak,
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
	rpcPrevOutputs := marshallTxOut(prevOutputs)

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.SignOutputRaw(rpcCtx,
		&signrpc.SignReq{
			RawTxBytes:  txRaw,
			SignDescs:   rpcSignDescs,
			PrevOutputs: rpcPrevOutputs,
		},
	)
	if err != nil {
		return nil, err
	}

	return resp.RawSigs, nil
}

// ComputeInputScript generates the proper input script for P2TR, P2WPKH and
// NP2WPKH outputs. This method only requires that the `Output`, `HashType`,
// `SigHashes` and `InputIndex` fields are populated within the sign
// descriptors. Passing in the previous outputs is required when spending one
// or more taproot (SegWit v1) outputs.
func (s *signerClient) ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*SignDescriptor, prevOutputs []*wire.TxOut) (
	[]*input.Script, error) {

	txRaw, err := encodeTx(tx)
	if err != nil {
		return nil, err
	}
	rpcSignDescs := marshallSignDescriptors(signDescriptors)
	rpcPrevOutputs := marshallTxOut(prevOutputs)

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.ComputeInputScript(
		rpcCtx, &signrpc.SignReq{
			RawTxBytes:  txRaw,
			SignDescs:   rpcSignDescs,
			PrevOutputs: rpcPrevOutputs,
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

// SignMessageOption is a function type that allows the customization of a
// SignMessage RPC request.
type SignMessageOption func(req *signrpc.SignMessageReq)

// SignCompact sets the flag for returning a compact signature in the message
// request.
func SignCompact() SignMessageOption {
	return func(req *signrpc.SignMessageReq) {
		req.CompactSig = true
	}
}

// SignSchnorr sets the flag for returning a Schnorr signature in the message
// request.
func SignSchnorr(taprootTweak []byte) SignMessageOption {
	return func(req *signrpc.SignMessageReq) {
		req.SchnorrSig = true
		req.SchnorrSigTapTweak = taprootTweak
	}
}

// SignMessage signs a message with the key specified in the key locator. The
// returned signature is fixed-size LN wire format encoded.
func (s *signerClient) SignMessage(ctx context.Context, msg []byte,
	locator keychain.KeyLocator, opts ...SignMessageOption) ([]byte, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcIn := &signrpc.SignMessageReq{
		Msg: msg,
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(locator.Family),
			KeyIndex:  int32(locator.Index),
		},
	}

	for _, opt := range opts {
		opt(rpcIn)
	}

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.SignMessage(rpcCtx, rpcIn)
	if err != nil {
		return nil, err
	}

	return resp.Signature, nil
}

// VerifyMessageOption is a function type that allows the customization of a
// VerifyMessage RPC request.
type VerifyMessageOption func(req *signrpc.VerifyMessageReq)

// VerifySchnorr sets the flag for checking a Schnorr signature in the message
// request.
func VerifySchnorr() VerifyMessageOption {
	return func(req *signrpc.VerifyMessageReq) {
		req.IsSchnorrSig = true
	}
}

// VerifyMessage verifies a signature over a message using the public key
// provided. The signature must be fixed-size LN wire format encoded.
func (s *signerClient) VerifyMessage(ctx context.Context, msg, sig []byte,
	pubkey [33]byte, opts ...VerifyMessageOption) (bool, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcIn := &signrpc.VerifyMessageReq{
		Msg:       msg,
		Signature: sig,
		Pubkey:    pubkey[:],
	}

	for _, opt := range opts {
		opt(rpcIn)
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
//	P_shared = privKeyNode * ephemeralPubkey
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

// MuSig2SessionOpts is the signature used to apply functional options to
// musig session requests.
type MuSig2SessionOpts func(*signrpc.MuSig2SessionRequest)

// noncesToBytes converts a set of public nonces to a [][]byte.
func noncesToBytes(nonces [][musig2.PubNonceSize]byte) [][]byte {
	nonceBytes := make([][]byte, len(nonces))

	for i := range nonces {
		nonceBytes[i] = nonces[i][:]
	}

	return nonceBytes
}

// MuSig2NonceOpt adds an optional set of nonces to a musig session request.
func MuSig2NonceOpt(nonces [][musig2.PubNonceSize]byte) MuSig2SessionOpts {
	return func(s *signrpc.MuSig2SessionRequest) {
		s.OtherSignerPublicNonces = noncesToBytes(nonces)
	}
}

// MuSig2TaprootTweakOpt adds an optional taproot tweak to the musig session
// request.
func MuSig2TaprootTweakOpt(scriptRoot []byte,
	keySpendOnly bool) MuSig2SessionOpts {

	return func(s *signrpc.MuSig2SessionRequest) {
		s.TaprootTweak = &signrpc.TaprootTweakDesc{
			ScriptRoot:   scriptRoot,
			KeySpendOnly: keySpendOnly,
		}
	}
}

// marshallMuSig2Version translates the passed input.MuSig2Version value to
// signrpc.MuSig2Version.
func marshallMuSig2Version(version input.MuSig2Version) (
	signrpc.MuSig2Version, error) {

	// Select the version based on the passed Go enum. Note that with new
	// versions added this switch must be updated as RPC enum values are
	// not directly mapped to the Go enum values defined in the input
	// package.
	switch version {
	case input.MuSig2Version040:
		return signrpc.MuSig2Version_MUSIG2_VERSION_V040, nil

	case input.MuSig2Version100RC2:
		return signrpc.MuSig2Version_MUSIG2_VERSION_V100RC2, nil

	default:
		return signrpc.MuSig2Version_MUSIG2_VERSION_UNDEFINED,
			fmt.Errorf("invalid MuSig2 version")
	}
}

// MuSig2CreateSession creates a new musig session with the key and signers
// provided.
func (s *signerClient) MuSig2CreateSession(ctx context.Context,
	version input.MuSig2Version, signerLoc *keychain.KeyLocator,
	signers [][]byte, opts ...MuSig2SessionOpts) (
	*input.MuSig2SessionInfo, error) {

	rpcMuSig2Version, err := marshallMuSig2Version(version)
	if err != nil {
		return nil, err
	}

	req := &signrpc.MuSig2SessionRequest{
		KeyLoc: &signrpc.KeyLocator{
			KeyFamily: int32(signerLoc.Family),
			KeyIndex:  int32(signerLoc.Index),
		},
		AllSignerPubkeys: signers,
		Version:          rpcMuSig2Version,
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

	session := &input.MuSig2SessionInfo{
		CombinedKey:   combinedKey,
		HaveAllNonces: resp.HaveAllNonces,
	}

	if len(resp.LocalPublicNonces) != musig2.PubNonceSize {
		return nil, fmt.Errorf("unexpected local nonce size: %v",
			len(resp.LocalPublicNonces))
	}
	copy(session.PublicNonce[:], resp.LocalPublicNonces)

	if len(resp.SessionId) != 32 {
		return nil, fmt.Errorf("unexpected session ID length: %v",
			len(resp.SessionId))
	}

	copy(session.SessionID[:], resp.SessionId)

	return session, nil
}

// MuSig2RegisterNonces registers additional public nonces for a musig2 session.
// It returns a boolean indicating whether we have all of our nonces present.
func (s *signerClient) MuSig2RegisterNonces(ctx context.Context,
	sessionID [32]byte, nonces [][66]byte) (bool, error) {

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
func (s *signerClient) MuSig2Sign(ctx context.Context, sessionID [32]byte,
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
func (s *signerClient) MuSig2CombineSig(ctx context.Context, sessionID [32]byte,
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

// MuSig2Cleanup allows a caller to clean up a session early in case where it's
// obvious that the signing session won't succeed and the resources can be
// released.
func (s *signerClient) MuSig2Cleanup(ctx context.Context,
	sessionID [32]byte) error {

	req := &signrpc.MuSig2CleanupRequest{
		SessionId: sessionID[:],
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.signerMac.WithMacaroonAuth(rpcCtx)
	_, err := s.client.MuSig2Cleanup(rpcCtx, req)

	return err
}
