package lndclient

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/stretchr/testify/require"
)

// newTestPubKey returns a fresh public key for use in descriptor
// marshalling tests.
func newTestPubKey(t *testing.T) *btcec.PublicKey {
	t.Helper()

	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	return priv.PubKey()
}

// TestMarshallSignDescriptorsFullLocator pins the contract of the full
// descriptor marshaller used by SignOutputRawKeyLocator: the key locator
// is attached whenever either the family or the index is non-zero, and
// only the all-zero KeyLocator (the struct's zero value, indistinguishable
// from "unset") is omitted.
//
// The family-6 / index-0 case is the regression guard. A prior version of
// the helper required both components to be non-zero and therefore
// silently dropped the locator for LND's node identity key slot. Signers
// that must re-derive the key from its locator — restored-from-seed
// wallets, or family-pinned identity keys — could not resolve the key.
func TestMarshallSignDescriptorsFullLocator(t *testing.T) {
	t.Parallel()

	pubKey := newTestPubKey(t)
	output := &wire.TxOut{PkScript: []byte{0x51}, Value: 1000}

	tests := []struct {
		name          string
		family        keychain.KeyFamily
		index         uint32
		expectLocator bool
	}{
		{
			name:          "all-zero locator is omitted",
			family:        0,
			index:         0,
			expectLocator: false,
		},
		{
			name:          "family-only locator is attached",
			family:        keychain.KeyFamilyNodeKey,
			index:         0,
			expectLocator: true,
		},
		{
			name:          "index-only locator is attached",
			family:        0,
			index:         5,
			expectLocator: true,
		},
		{
			name:          "fully populated locator is attached",
			family:        keychain.KeyFamilyNodeKey,
			index:         5,
			expectLocator: true,
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			descs := []*SignDescriptor{{
				KeyDesc: keychain.KeyDescriptor{
					PubKey: pubKey,
					KeyLocator: keychain.KeyLocator{
						Family: tc.family,
						Index:  tc.index,
					},
				},
				Output: output,
			}}

			rpcDescs := marshallSignDescriptors(descs, true)
			require.Len(t, rpcDescs, 1)

			rpcKeyDesc := rpcDescs[0].KeyDesc
			require.NotNil(t, rpcKeyDesc)
			require.Equal(t,
				pubKey.SerializeCompressed(),
				rpcKeyDesc.RawKeyBytes,
			)

			if !tc.expectLocator {
				require.Nil(t, rpcKeyDesc.KeyLoc)
				return
			}

			require.NotNil(t, rpcKeyDesc.KeyLoc)
			require.Equal(t,
				int32(tc.family),
				rpcKeyDesc.KeyLoc.KeyFamily,
			)
			require.Equal(t,
				int32(tc.index),
				rpcKeyDesc.KeyLoc.KeyIndex,
			)
		})
	}
}

// TestMarshallSignDescriptorsPartialOnlyOneOf verifies that the partial
// descriptor marshaller used by the legacy SignOutputRaw populates either
// the public key or the locator, but never both. Applications like Loop
// have adjusted themselves to this behavior, which is why the fix for the
// locator-drop bug lives in the full descriptor path instead.
func TestMarshallSignDescriptorsPartialOnlyOneOf(t *testing.T) {
	t.Parallel()

	pubKey := newTestPubKey(t)
	output := &wire.TxOut{PkScript: []byte{0x51}, Value: 1000}

	t.Run("pubkey present drops locator", func(t *testing.T) {
		t.Parallel()

		descs := []*SignDescriptor{{
			KeyDesc: keychain.KeyDescriptor{
				PubKey: pubKey,
				KeyLocator: keychain.KeyLocator{
					Family: keychain.KeyFamilyNodeKey,
					Index:  5,
				},
			},
			Output: output,
		}}

		rpcDescs := marshallSignDescriptors(descs, false)
		require.Len(t, rpcDescs, 1)

		require.Equal(t,
			pubKey.SerializeCompressed(),
			rpcDescs[0].KeyDesc.RawKeyBytes,
		)
		require.Nil(t, rpcDescs[0].KeyDesc.KeyLoc)
	})

	t.Run("pubkey absent uses locator", func(t *testing.T) {
		t.Parallel()

		descs := []*SignDescriptor{{
			KeyDesc: keychain.KeyDescriptor{
				KeyLocator: keychain.KeyLocator{
					Family: keychain.KeyFamilyNodeKey,
					Index:  5,
				},
			},
			Output: output,
		}}

		rpcDescs := marshallSignDescriptors(descs, false)
		require.Len(t, rpcDescs, 1)

		require.Empty(t, rpcDescs[0].KeyDesc.RawKeyBytes)
		require.NotNil(t, rpcDescs[0].KeyDesc.KeyLoc)
		require.Equal(t,
			int32(keychain.KeyFamilyNodeKey),
			rpcDescs[0].KeyDesc.KeyLoc.KeyFamily,
		)
		require.Equal(t,
			int32(5),
			rpcDescs[0].KeyDesc.KeyLoc.KeyIndex,
		)
	})
}
