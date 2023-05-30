package lndclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon.v2"
)

// TestMacaroonServiceMigration tests that a client that was using a macaroon
// service encrypted with an empty passphrase can successfully migrate to
// using a shared key passphrase.
func TestMacaroonServiceMigration(t *testing.T) {
	// Create a temporary directory where we can store the macaroon db and
	// the macaroon we are about to create.
	tempDirPath := t.TempDir()

	rks, backend, err := NewBoltMacaroonStore(
		tempDirPath, "macaroons.db", defaultMacaroonTimeout,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, backend.Close())
	}()

	mockPerms := map[string][]bakery.Op{
		"/uri.Test/WriteTest": {
			bakery.Op{Entity: "entity", Action: "write"},
		},
	}

	// The initial config we will use has an empty DB password.
	cfg := &MacaroonServiceConfig{
		MacaroonLocation: "testLocation",
		MacaroonPath:     filepath.Join(tempDirPath, "test.macaroon"),
		DBPassword:       []byte{},
		RootKeyStore:     rks,
		RequiredPerms:    mockPerms,
	}

	// Create a new macaroon service with an empty password.
	testService, err := createTestService(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, testService.stop())
	}()

	err = testService.CreateUnlock(&cfg.DBPassword)
	require.NoError(t, err)

	// We generate a new root key. This is required for the call the
	// ChangePassword to succeed.
	err = testService.GenerateNewRootKey()
	require.NoError(t, err)

	// Close the test db.
	err = testService.stop()
	require.NoError(t, err)

	// Now we will restart the DB but using the new MacaroonService Start
	// function which will attempt to upgrade our db to be encrypted with
	// a shared secret with LND if we give an empty DB password.
	cfg.EphemeralKey = SharedKeyNUMS
	cfg.KeyLocator = SharedKeyLocator
	sharedSecret := []byte("shared secret")
	cfg.LndClient = &LndServices{Signer: &mockSignerClient{
		sharedKey: sharedSecret,
	}}

	ms, err := NewMacaroonService(cfg)
	require.NoError(t, err)

	// We now start the service. This will attempt to unlock the db using
	// the shared secret with LND. This will initially fail and so
	// decryption with an empty passphrase will be attempted. If this
	// succeeds, then the db will be re-encrypted with the new shared
	// secret.
	require.NoError(t, ms.Start())
	require.NoError(t, ms.Stop())

	// To test that the db has been successfully re-encrypted with the new
	// key, we remove the connection to lnd and use the shared secret
	// directly as the new DB password.
	cfg.EphemeralKey = nil
	cfg.KeyLocator = nil
	cfg.LndClient = nil
	cfg.DBPassword = sharedSecret
	ms, err = NewMacaroonService(cfg)
	require.NoError(t, err)

	require.NoError(t, ms.Start())
	require.NoError(t, ms.Stop())
}

// TestMacaroonGeneration tests that the macaroon service generates macaroons
// as expected. A macaroon should not be regenerated if the required permissions
// haven't changed, and should be regenerated if the required permissions have
// changed.
func TestMacaroonGeneration(t *testing.T) {
	// Create a temporary directory where we can store the macaroon db and
	// the macaroon we are about to create.
	tempDirPath := t.TempDir()
	macaroonPath := filepath.Join(tempDirPath, "test.macaroon")

	// Set the macaroon store, and get the root key store.
	rks, backend, err := NewBoltMacaroonStore(
		tempDirPath, "macaroons.db", defaultMacaroonTimeout,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})

	// Generate mock permissions.
	firstEntityWrite := bakery.Op{Entity: "first_entity", Action: "write"}
	firstEntityRead := bakery.Op{Entity: "first_entity", Action: "read"}
	secondEntityRead := bakery.Op{Entity: "second_entity", Action: "read"}

	// We'll initially use only part of the mock permissions, but we'll
	// modify the required permissions later for the different tests.
	mockPerms := map[string][]bakery.Op{
		"/uri1.Test/WriteTest": {firstEntityWrite},
		"/uri1.Test/ReadTest":  {firstEntityRead},
	}

	// Create the initial config. We set a mock DB password, as we either
	// need to set a DB password or provide an LndClient, ephemeral key and
	// key locator so that a shared key can be derived with LND, when
	// creating the macaroon service.
	cfg := &MacaroonServiceConfig{
		MacaroonLocation: tempDirPath,
		DBPassword:       []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		RootKeyStore:     rks,
		MacaroonPath:     macaroonPath,
		RequiredPerms:    mockPerms,
	}

	// Create the macaroon service we will use for creating macaroons.
	ms, err := NewMacaroonService(cfg)
	require.NoError(t, err)

	// Firstly, we test that will fail to create a macaroon if we set
	// the MacaroonPath to an existing file which isn't a macaroon.
	cfg.MacaroonPath = filepath.Join(tempDirPath, "macaroons.db")

	require.Error(t, ms.Start())
	require.NoError(t, ms.Stop())

	// Reset the macaroon path to the correct path.
	cfg.MacaroonPath = macaroonPath

	// We'll now test that the macaroon service generates and regenerates
	// macaroons as expected. We'll do this in several steps:

	// 1. With the 2 required permissions in the mockPerms map, we ensure
	// that the macaroon service generates a macaroon with the correct
	// permissions.
	require.NoError(t, ms.Start())

	// Ensure that the created macaroon has the correct permissions.
	mac1, mac1Bytes := extractMacaroon(t, cfg.MacaroonPath)
	assertMacaroonPerms(t, mac1, mockPerms, ms)

	require.NoError(t, ms.Stop())

	// 2. Secondly, we use the same permissions as last time, and ensure
	// that the macaroon hasn't been regenerated.
	require.NoError(t, ms.Start())
	_, mac2Bytes := extractMacaroon(t, cfg.MacaroonPath)

	// We then ensure that the macaroon bytes from the mac1 is the same as
	// mac2. This ensures that the macaroon service didn't regenerate the
	// macaroon, as if it did, the macaroon bytes would be different as
	// another nonce would have been used when generating mac2, resulting
	// in different macaroon bytes (the signature for the macaroon would
	// also differ).
	require.Equal(t, mac1Bytes, mac2Bytes)
	require.NoError(t, ms.Stop())

	// 3. Thirdly, we add a permission to the mockPerms map that is for a
	// different URI, but that requires an entity and action we already have
	// in the previous macaroon. We then ensure that the macaroon service
	// doesn't regenerate the macaroon, as it's not needed.
	mockPerms["/uri2.Test/WriteTest"] = []bakery.Op{firstEntityWrite}
	cfg.RequiredPerms = mockPerms

	require.NoError(t, ms.Start())

	_, mac3Bytes := extractMacaroon(t, cfg.MacaroonPath)

	require.Equal(t, mac1Bytes, mac3Bytes)
	require.NoError(t, ms.Stop())

	// 4. Fourthly, we add a permission to the mockPerms map that requires a
	// new entity and action which the previous macaroon doesn't contain. We
	// then ensure that the macaroon service regenerates the macaroon, as
	// it's needed.
	mockPerms["/uri3.Test/ReadTest"] = []bakery.Op{secondEntityRead}
	cfg.RequiredPerms = mockPerms

	require.NoError(t, ms.Start())
	mac4, mac4Bytes := extractMacaroon(t, cfg.MacaroonPath)
	assertMacaroonPerms(t, mac4, mockPerms, ms)

	// We're already sure that the macaroon service regenerated the macaroon
	// as assertMacaroonPerms would have failed if it didn't, but we
	// also ensure that the macaroon bytes are different from the previous
	// macaroon just to clarify that it was regenerated.
	require.NotEqual(t, mac1Bytes, mac4Bytes)
	require.NoError(t, ms.Stop())

	// 5. Fifthly, we remove a required entity. We then ensure that the
	// macaroon service also regenerates the macaroon, even though the
	// previous macaroon already contains the required permissions, as we'd
	// like to ensure that we don't keep unnecessary permissions no longer
	// required in the macaroon.
	delete(mockPerms, "/uri3.Test/ReadTest")
	cfg.RequiredPerms = mockPerms

	require.NoError(t, ms.Start())
	mac5, mac5Bytes := extractMacaroon(t, cfg.MacaroonPath)
	assertMacaroonPerms(t, mac5, mockPerms, ms)

	require.NotEqual(t, mac4Bytes, mac5Bytes)
	require.NoError(t, ms.Stop())

	// 6. Lastly, we keep the same permissions, but change the macaroon
	// path. We then ensure that the macaroon service crates a new macaroon.
	cfg.MacaroonPath = filepath.Join(tempDirPath, "test2.macaroon")

	require.NoError(t, ms.Start())
	mac6, mac6Bytes := extractMacaroon(t, cfg.MacaroonPath)
	assertMacaroonPerms(t, mac6, mockPerms, ms)

	require.NotEqual(t, mac5Bytes, mac6Bytes)
	require.NoError(t, ms.Stop())
}

type testMacaroonService struct {
	*macaroons.Service
	rks bakery.RootKeyStore
}

func createTestService(cfg *MacaroonServiceConfig) (*testMacaroonService,
	error) {

	// Create the macaroon authentication/authorization service.
	service, err := macaroons.NewService(
		cfg.RootKeyStore, cfg.MacaroonLocation, cfg.StatelessInit,
		cfg.Checkers...,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to set up macaroon "+
			"service: %v", err)
	}

	return &testMacaroonService{
		Service: service,
		rks:     cfg.RootKeyStore,
	}, nil
}

func (s *testMacaroonService) stop() error {
	var returnErr error
	if eRKS, ok := s.rks.(macaroons.ExtendedRootKeyStore); ok {
		if err := eRKS.Close(); err != nil {
			returnErr = err
		}
	}

	if err := s.Close(); err != nil {
		returnErr = err
	}

	return returnErr
}

type mockSignerClient struct {
	sharedKey []byte

	SignerClient
}

func (m *mockSignerClient) DeriveSharedKey(_ context.Context,
	_ *btcec.PublicKey, _ *keychain.KeyLocator) ([32]byte, error) {

	var res [32]byte
	copy(res[:], m.sharedKey)

	return res, nil
}

func extractMacaroon(t *testing.T,
	macaroonPath string) (*macaroon.Macaroon, []byte) {

	macBytes, err := os.ReadFile(macaroonPath)
	require.NoError(t, err)

	mac := &macaroon.Macaroon{}
	err = mac.UnmarshalBinary(macBytes)
	require.NoError(t, err)

	return mac, macBytes
}

func assertMacaroonPerms(t *testing.T, mac *macaroon.Macaroon,
	expectedPermissions map[string][]bakery.Op, ms *MacaroonService) {

	var (
		authChecker   = ms.Checker.Auth(macaroon.Slice{mac})
		expectedPerms = extractPerms(expectedPermissions)
	)

	allowedInfo, err := authChecker.Allowed(context.Background())
	require.NoError(t, err)

	for _, expectedPerm := range expectedPerms {
		require.Contains(t, allowedInfo.OpIndexes, expectedPerm)
	}
	require.Len(t, allowedInfo.OpIndexes, len(expectedPerms))
}
