package lndclient

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/kvdb"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/stretchr/testify/require"
)

// TestMacaroonServiceMigration tests that a client that was using a macaroon
// service encrypted with an empty passphrase can successfully migrate to
// using a shared key passphrase.
func TestMacaroonServiceMigration(t *testing.T) {
	// Create a temporary directory where we can store the macaroon db
	// we are about to create.
	tempDirPath, err := ioutil.TempDir("", ".testMacaroons")
	require.NoError(t, err)
	defer os.RemoveAll(tempDirPath)

	// The initial config we will use has an empty DB password.
	cfg := &MacaroonServiceConfig{
		DBPath:           tempDirPath,
		DBFileName:       "macaroons.db",
		DBTimeout:        defaultDBTimeout,
		MacaroonLocation: "testLocation",
		MacaroonPath:     tempDirPath,
		DBPassword:       []byte{},
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

type testMacaroonService struct {
	*macaroons.Service
	db kvdb.Backend
}

func createTestService(cfg *MacaroonServiceConfig) (*testMacaroonService,
	error) {

	db, err := kvdb.GetBoltBackend(&kvdb.BoltBackendConfig{
		DBPath:     cfg.DBPath,
		DBFileName: cfg.DBFileName,
		DBTimeout:  cfg.DBTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to load macaroon db: "+
			"%v", err)
	}

	// Create the macaroon authentication/authorization service.
	service, err := macaroons.NewService(
		db, cfg.MacaroonLocation, cfg.StatelessInit, cfg.Checkers...,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to set up macaroon "+
			"service: %v", err)
	}

	return &testMacaroonService{
		Service: service,
		db:      db,
	}, nil
}

func (s *testMacaroonService) stop() error {
	var returnErr error
	if err := s.db.Close(); err != nil {
		returnErr = err
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
