package lndclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
)

// StateClient exposes base lightning functionality.
type StateClient interface {
	ServiceClient[lnrpc.StateClient]

	// SubscribeState subscribes to the current state of the wallet.
	SubscribeState(ctx context.Context) (chan WalletState, chan error,
		error)

	// GetState returns the current wallet state without subscribing to more
	// state updates.
	GetState(context.Context) (WalletState, error)
}

// WalletState is a type that represents all states the lnd wallet can be in.
type WalletState uint8

const (
	// WalletStateNonExisting denotes that no wallet has been created in lnd
	// so far.
	WalletStateNonExisting WalletState = 0

	// WalletStateLocked denotes that a wallet exists in lnd but it has not
	// yet been unlocked.
	WalletStateLocked WalletState = 1

	// WalletStateUnlocked denotes that a wallet exists in lnd and it has
	// been unlocked but the RPC server isn't yet fully started up.
	WalletStateUnlocked WalletState = 2

	// WalletStateRPCActive denotes that lnd is now fully ready to receive
	// RPC requests other than wallet unlocking operations.
	WalletStateRPCActive WalletState = 3

	// WalletStateServerActive denotes that lnd's main server is now fully
	// ready to receive calls.
	WalletStateServerActive WalletState = 4

	// WalletStateWaitingToStart indicates that lnd is at the beginning of
	// the startup process. In a cluster environment this may mean that
	// we're waiting to become the leader in which case RPC calls will be
	// disabled until this instance has been elected as leader.
	WalletStateWaitingToStart WalletState = 255
)

// String returns a string representation of the WalletState.
func (s WalletState) String() string {
	switch s {
	case WalletStateNonExisting:
		return "No wallet exists"

	case WalletStateLocked:
		return "Wallet is locked"

	case WalletStateUnlocked:
		return "Wallet is unlocked"

	case WalletStateRPCActive:
		return "Lnd RPC server is ready for requests"

	case WalletStateServerActive:
		return "Lnd main server is ready for requests"

	case WalletStateWaitingToStart:
		return "Lnd is waiting to start"

	default:
		return fmt.Sprintf("unknown wallet state <%d>", s)
	}
}

// ReadyForGetInfo returns true if the wallet state is ready for the GetInfo to
// be called. This needs to also return true for the RPC active state to be
// backward compatible with lnd 0.13.x nodes which didn't yet have the server
// active state. But the GetInfo RPC isn't guarded by that server active flag
// anyway, so we can call that whenever the RPC server is ready.
func (s WalletState) ReadyForGetInfo() bool {
	return s == WalletStateRPCActive || s == WalletStateServerActive
}

// stateClient is a client for lnd's lnrpc.State service.
type stateClient struct {
	client      lnrpc.StateClient
	readonlyMac serializedMacaroon
	timeout     time.Duration

	wg sync.WaitGroup
}

// A compile time check to ensure that stateClient implements the StateClient
// interface.
var _ StateClient = (*stateClient)(nil)

// newStateClient returns a new stateClient.
func newStateClient(conn grpc.ClientConnInterface,
	readonlyMac serializedMacaroon, timeout time.Duration) *stateClient {

	return &stateClient{
		client:      lnrpc.NewStateClient(conn),
		readonlyMac: readonlyMac,
		timeout:     timeout,
	}
}

// WaitForFinished waits until all state subscriptions have finished.
func (s *stateClient) WaitForFinished() {
	s.wg.Wait()
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (s *stateClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	lnrpc.StateClient) {

	return s.readonlyMac.WithMacaroonAuth(parentCtx), s.timeout, s.client
}

// SubscribeState subscribes to the current state of the wallet.
func (s *stateClient) SubscribeState(ctx context.Context) (chan WalletState,
	chan error, error) {

	resp, err := s.client.SubscribeState(
		ctx, &lnrpc.SubscribeStateRequest{},
	)
	if err != nil {
		return nil, nil, err
	}

	stateChan := make(chan WalletState, 1)
	errChan := make(chan error, 1)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			stateEvent, err := resp.Recv()
			if err != nil {
				errChan <- err
				return
			}

			state, err := unmarshalWalletState(stateEvent.State)
			if err != nil {
				errChan <- err
				return
			}

			select {
			case stateChan <- state:
			case <-ctx.Done():
				return
			}

			// If this is the final state, no more states will be
			// sent to us, and we can close the subscription.
			if state == WalletStateServerActive {
				close(stateChan)
				close(errChan)

				return
			}
		}
	}()

	return stateChan, errChan, nil
}

// GetState returns the current wallet state without subscribing to more
// state updates.
func (s *stateClient) GetState(ctx context.Context) (WalletState, error) {
	state, err := s.client.GetState(ctx, &lnrpc.GetStateRequest{})
	if err != nil {
		return 0, err
	}

	return unmarshalWalletState(state.State)
}

// unmarshalWalletState turns the RPC wallet state into the internal wallet
// state type.
func unmarshalWalletState(rpcState lnrpc.WalletState) (WalletState, error) {
	switch rpcState {
	case lnrpc.WalletState_WAITING_TO_START:
		return WalletStateWaitingToStart, nil

	case lnrpc.WalletState_NON_EXISTING:
		return WalletStateNonExisting, nil

	case lnrpc.WalletState_LOCKED:
		return WalletStateLocked, nil

	case lnrpc.WalletState_UNLOCKED:
		return WalletStateUnlocked, nil

	case lnrpc.WalletState_RPC_ACTIVE:
		return WalletStateRPCActive, nil

	case lnrpc.WalletState_SERVER_ACTIVE:
		return WalletStateServerActive, nil

	default:
		return 0, fmt.Errorf("unknown wallet state: %d", rpcState)
	}
}
