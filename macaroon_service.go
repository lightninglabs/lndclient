package lndclient

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/kvdb"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/lightningnetwork/lnd/rpcperms"
	"go.etcd.io/bbolt"
	"google.golang.org/grpc"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
)

const (
	// defaultDBName is the default macaroon db name.
	defaultDBName = "macaroons.db"

	// defaultDBTimeout is the default timeout to be used for the
	// macaroon db connection.
	defaultDBTimeout = 5 * time.Second
)

var (
	// sharedKeyNUMSBytes holds the bytes representing the compressed
	// byte encoding of SharedKeyNUMS. It was generated via a
	// try-and-increment approach using the phrase "Shared Secret" with
	// SHA2-256. The code for the try-and-increment approach can be seen
	// here: https://github.com/lightninglabs/lightning-node-connect/tree/master/mailbox/numsgen
	sharedKeyNUMSBytes, _ = hex.DecodeString(
		"0215b5a3e0ef58b101431e1e513dd017d1018b420bd2e89dcd71f45c031f00469e",
	)

	// SharedKeyNUMS is the public key point that can be use for when we
	// are deriving a shared secret key with LND.
	SharedKeyNUMS, _ = btcec.ParsePubKey(sharedKeyNUMSBytes)

	// SharedKeyLocator is a key locator that can be used for deriving a
	// shared secret key with LND.
	SharedKeyLocator = &keychain.KeyLocator{
		Family: 21,
		Index:  0,
	}
)

// MacaroonService handles the creatation and unlocking of a macaroon DB file.
// It is also a wrapper for a macaroon.Service and uses this to create a
// default macaroon for the caller if stateless mode has not been specified.
type MacaroonService struct {
	cfg *MacaroonServiceConfig

	db kvdb.Backend

	*macaroons.Service
}

// MacaroonServiceConfig holds configuration values used by the MacaroonService.
type MacaroonServiceConfig struct {
	// DBPath is the path to where the macaroon db file will be stored.
	DBPath string

	// DBFile is the name of the macaroon db.
	DBFileName string

	// DBTimeout is the maximum time we wait for the bbolt database to be
	// opened.
	DBTimeout time.Duration

	// MacaroonLocation is the value used for a macaroons' "Location" field.
	MacaroonLocation string

	// MacaroonPath is the path to where macaroons should be stored.
	MacaroonPath string

	// StatelessInit should be set to true if no default macaroons should
	// be created and stored on disk.
	StatelessInit bool

	// Checkers are used to add extra validation checks on macaroon.
	Checkers []macaroons.Checker

	// RequiredPerms defines all method paths and the permissions required
	// when accessing those paths.
	RequiredPerms map[string][]bakery.Op

	// Caveats is a list of caveats that will be added to the default
	// macaroons.
	Caveats []checkers.Caveat

	// DBPassword is the password that will be used to encrypt the macaroon
	// db. If DBPassword is not set, then LndClient, EphemeralKey and
	// KeyLocator must be set instead.
	DBPassword []byte

	// LndClient is an LND client that can be used for any lnd queries.
	// This only needs to be set if DBPassword is not set.
	LndClient *LndServices

	// RPCTimeout is the time after which an RPC call will be canceled if
	// it has not yet completed.
	RPCTimeout time.Duration

	// EphemeralKey is a key that will be used to derive a shared secret
	// with LND. This only needs to be set if DBPassword is not set.
	EphemeralKey *btcec.PublicKey

	// KeyLocator is the locator used to derive a shared secret with LND.
	// This only needs to be set if DBPassword is not set.
	KeyLocator *keychain.KeyLocator
}

// NewMacaroonService checks the config values passed in and creates a
// MacaroonService object accordingly.
func NewMacaroonService(cfg *MacaroonServiceConfig) (*MacaroonService, error) {
	// Validate config.
	if cfg.DBPath == "" {
		return nil, errors.New("no macaroon db path provided")
	}

	if cfg.MacaroonLocation == "" {
		return nil, errors.New("no macaroon location provided")
	}

	if cfg.DBFileName == "" {
		cfg.DBFileName = defaultDBName
	}

	if cfg.DBTimeout == 0 {
		cfg.DBTimeout = defaultDBTimeout
	} else if cfg.DBTimeout < 0 {
		return nil, errors.New("can't have a negative db timeout")
	}

	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = defaultRPCTimeout
	} else if cfg.RPCTimeout < 0 {
		return nil, errors.New("can't have a negative rpc timeout")
	}

	if !cfg.StatelessInit && cfg.MacaroonPath == "" {
		return nil, errors.New("a macaroon path must be given if we " +
			"are not in stateless mode")
	}

	ms := MacaroonService{
		cfg: cfg,
	}

	if len(cfg.DBPassword) != 0 {
		return &ms, nil
	}

	if cfg.LndClient == nil || cfg.EphemeralKey == nil ||
		cfg.KeyLocator == nil {

		return nil, errors.New("must provide an LndClient, ephemeral " +
			"key and key locator if no DBPassword is provided " +
			"so that a shared key can be derived with LND")
	}

	return &ms, nil
}

// Start starts the macaroon validation service, creates or unlocks the macaroon
// database and, if we are not in stateless mode, creates the default macaroon
// if it doesn't exist yet.
func (ms *MacaroonService) Start() error {
	// Open the database.
	var err error
	ms.db, err = kvdb.GetBoltBackend(&kvdb.BoltBackendConfig{
		DBPath:     ms.cfg.DBPath,
		DBFileName: ms.cfg.DBFileName,
		DBTimeout:  ms.cfg.DBTimeout,
	})
	if err == bbolt.ErrTimeout {
		return fmt.Errorf("error while trying to open %s/%s: "+
			"timed out after %v when trying to obtain exclusive "+
			"lock - make sure no other daemon process "+
			"(standalone or embedded in lightning-terminal) is "+
			"trying to access this db", ms.cfg.DBPath,
			ms.cfg.DBFileName, ms.cfg.DBTimeout)
	}
	if err != nil {
		return fmt.Errorf("unable to load macaroon db: %v", err)
	}

	// Create the macaroon authentication/authorization service.
	service, err := macaroons.NewService(
		ms.db, ms.cfg.MacaroonLocation, ms.cfg.StatelessInit,
		ms.cfg.Checkers...,
	)
	if err != nil {
		return fmt.Errorf("unable to set up macaroon service: %v", err)
	}
	ms.Service = service

	switch {
	case len(ms.cfg.DBPassword) != 0:
		// If a non-empty DB password was provided, then use this
		// directly to try and unlock the db.
		err := ms.CreateUnlock(&ms.cfg.DBPassword)
		if err != nil {
			return fmt.Errorf("unable to unlock macaroon DB: %v",
				err)
		}

	default:
		// If an empty DB password was provided, we want to establish a
		// shared secret with LND which we will use as our DB password.
		ctx, cancel := context.WithTimeout(
			context.Background(), ms.cfg.RPCTimeout,
		)
		defer cancel()

		sharedKey, err := ms.cfg.LndClient.Signer.DeriveSharedKey(
			ctx, ms.cfg.EphemeralKey, ms.cfg.KeyLocator,
		)
		if err != nil {
			return fmt.Errorf("unable to derive a shared "+
				"secret with LND: %v", err)
		}

		// Try to unlock the macaroon store with the shared key.
		dbPassword := sharedKey[:]
		err = ms.CreateUnlock(&dbPassword)
		if err == nil {
			// If the db was successfully unlocked, we can continue.
			break
		}

		log.Infof("Macaroon DB could not be unlocked with the " +
			"derived shared key. Attempting to unlock with " +
			"empty password instead")

		// Otherwise, we will attempt to unlock the db with an empty
		// password. If this succeeds, we will re-encrypt it with
		// the shared key.
		dbPassword = []byte{}
		err = ms.CreateUnlock(&dbPassword)
		if err != nil {
			return fmt.Errorf("unable to unlock macaroon DB: %v",
				err)
		}

		log.Infof("Re-encrypting macaroon DB with derived shared key")

		// Attempt to now re-encrypt the DB with the shared key.
		err = ms.ChangePassword(dbPassword, sharedKey[:])
		if err != nil {
			return fmt.Errorf("unable to change the macaroon "+
				"DB password: %v", err)
		}
	}

	// There are situations in which we don't want a macaroon to be created
	// on disk (for example when running inside LiT stateless integrated
	// mode). For any other cases, we create macaroon files in the default
	// directory.
	if ms.cfg.StatelessInit || lnrpc.FileExists(ms.cfg.MacaroonPath) {
		return nil
	}

	// We don't offer the ability to rotate macaroon root keys yet, so just
	// use the default one since the service expects some value to be set.
	idCtx := macaroons.ContextWithRootKeyID(
		context.Background(), macaroons.DefaultRootKeyID,
	)

	// We only generate one default macaroon that contains all existing
	// permissions (equivalent to the admin.macaroon in lnd). Custom
	// macaroons can be created through the bakery RPC.
	mac, err := ms.Oven.NewMacaroon(
		idCtx, bakery.LatestVersion, ms.cfg.Caveats,
		extractPerms(ms.cfg.RequiredPerms)...,
	)
	if err != nil {
		return err
	}

	macBytes, err := mac.M().MarshalBinary()
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(ms.cfg.MacaroonPath, macBytes, 0644)
	if err != nil {
		if err := os.Remove(ms.cfg.MacaroonPath); err != nil {
			log.Errorf("Unable to remove %s: %v",
				ms.cfg.MacaroonPath, err)
		}
	}

	return err
}

// Stop cleans up the MacaroonService.
func (ms *MacaroonService) Stop() error {
	var shutdownErr error
	if err := ms.Close(); err != nil {
		log.Errorf("Error closing macaroon service: %v", err)
		shutdownErr = err
	}

	if err := ms.db.Close(); err != nil {
		log.Errorf("Error closing macaroon DB: %v", err)
		shutdownErr = err
	}

	return shutdownErr
}

// extractPerms creates a deduped list of all the perms in a required perms map.
func extractPerms(requiredPerms map[string][]bakery.Op) []bakery.Op {
	entityActionPairs := make(map[string]map[string]struct{})

	for _, perms := range requiredPerms {
		for _, p := range perms {
			if _, ok := entityActionPairs[p.Entity]; !ok {
				entityActionPairs[p.Entity] = make(
					map[string]struct{},
				)
			}

			entityActionPairs[p.Entity][p.Action] = struct{}{}
		}
	}

	// Dedup the permissions.
	perms := make([]bakery.Op, 0)
	for entity, actions := range entityActionPairs {
		for action := range actions {
			perms = append(perms, bakery.Op{
				Entity: entity,
				Action: action,
			})
		}
	}

	return perms
}

// Interceptors creates gRPC server options with the macaroon security
// interceptors.
func (ms *MacaroonService) Interceptors() (grpc.UnaryServerInterceptor,
	grpc.StreamServerInterceptor, error) {

	interceptor := rpcperms.NewInterceptorChain(log, false, nil)
	err := interceptor.Start()
	if err != nil {
		return nil, nil, err
	}

	interceptor.SetWalletUnlocked()
	interceptor.AddMacaroonService(ms.Service)
	for method, permissions := range ms.cfg.RequiredPerms {
		err := interceptor.AddPermission(method, permissions)
		if err != nil {
			return nil, nil, err
		}
	}

	unaryInterceptor := interceptor.MacaroonUnaryServerInterceptor()
	streamInterceptor := interceptor.MacaroonStreamServerInterceptor()
	return unaryInterceptor, streamInterceptor, nil
}
