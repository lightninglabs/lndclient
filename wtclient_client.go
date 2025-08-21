package lndclient

import (
	"context"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc/wtclientrpc"
	"google.golang.org/grpc"
)

// WatchtowerClientClient implements the watchtower client interface
// https://lightning.engineering/api-docs/category/watchtowerclient-service/
// NOTE: For the macaroon code to work correctly, this needs to be named
// WatchtowerClientClient since we use reflection to set macaroon permissions.
type WatchtowerClientClient interface {
	ServiceClient[wtclientrpc.WatchtowerClientClient]

	// AddTower adds a new watchtower reachable at the given address and
	// considers it for new sessions. If the watchtower already exists, then
	// any new addresses included will be considered when dialing it for
	// session negotiations and backups.
	AddTower(ctx context.Context, pubkey []byte, address string) error

	// DeactivateTower sets the given tower's status to inactive so that it
	// is not considered for session negotiation. Its sessions will also not
	// be used while the tower is inactive.
	DeactivateTower(ctx context.Context, pubkey []byte) (string, error)

	// GetTowerInfo gets information about a watchtower that corresponds to
	// the given pubkey. The `includeSessions` flag controls whether session
	// information is included. The `excludeExhaustedSessions` controls
	// whether exhausted sessions are included in the response.
	GetTowerInfo(ctx context.Context, pubkey []byte, includeSessions,
		excludeExhaustedSessions bool) (*wtclientrpc.Tower, error)

	// ListTowers gets information about all registered watchtowers. The
	// `includeSessions` and `excludeExhaustedSessions` flags serve the same
	// function as in the `GetTowerInfo` method.
	ListTowers(ctx context.Context, includeSessions,
		excludeExhaustedSessions bool) ([]*wtclientrpc.Tower, error)

	// Policy returns the active watchtower client policy configuration.
	Policy(ctx context.Context, policyType wtclientrpc.PolicyType) (
		*wtclientrpc.PolicyResponse, error)

	// RemoveTower removes a watchtower from being considered for future
	// session negotiations and from being used for any subsequent backups
	// until it's added again. If an address is provided, then this RPC only
	// serves as a way of removing the address from the watchtower instead.
	RemoveTower(ctx context.Context, pubkey []byte, address string) error

	// Stats returns the in-memory statistics of the client since startup.
	Stats(ctx context.Context) (*wtclientrpc.StatsResponse, error)

	// TerminateSession terminates the given session and marks it to not be
	// used for backups anymore. Returns a human-readable status string.
	TerminateSession(ctx context.Context, sessionId []byte) (string, error)
}

type wtClient struct {
	client      wtclientrpc.WatchtowerClientClient
	wtClientMac serializedMacaroon
	timeout     time.Duration
}

// A compile time check to ensure that wtClientClient implements the
// WtclientClient interface.
var _ WatchtowerClientClient = (*wtClient)(nil)

// newClientClient creates a new watchtower client interface.
func newWtClient(conn grpc.ClientConnInterface, wtClientMac serializedMacaroon,
	timeout time.Duration) *wtClient {

	return &wtClient{
		client:      wtclientrpc.NewWatchtowerClientClient(conn),
		wtClientMac: wtClientMac,
		timeout:     timeout,
	}
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (m *wtClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	wtclientrpc.WatchtowerClientClient) {

	return m.wtClientMac.WithMacaroonAuth(parentCtx), m.timeout, m.client
}

// AddTower adds a new watchtower reachable at the given address and
// considers it for new sessions. If the watchtower already exists, then
// any new addresses included will be considered when dialing it for
// session negotiations and backups.
func (m *wtClient) AddTower(ctx context.Context, pubkey []byte,
	address string) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcReq := &wtclientrpc.AddTowerRequest{
		Pubkey:  pubkey,
		Address: address,
	}

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	_, err := m.client.AddTower(rpcCtx, rpcReq)

	return err
}

// DeactivateTower sets the given tower's status to inactive so that it
// is not considered for session negotiation. Its sessions will also not
// be used while the tower is inactive.
func (m *wtClient) DeactivateTower(ctx context.Context, pubkey []byte) (
	string, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.DeactivateTower(rpcCtx,
		&wtclientrpc.DeactivateTowerRequest{
			Pubkey: pubkey,
		},
	)
	if err != nil {
		return "", err
	}

	return resp.Status, nil
}

// GetTowerInfo gets information about a watchtower that corresponds to
// the given pubkey. The `includeSessions` flag controls whether session
// information is included. The `excludeExhaustedSessions` controls
// whether exhausted sessions are included in the response.
func (m *wtClient) GetTowerInfo(ctx context.Context, pubkey []byte,
	includeSessions, excludeExhaustedSessions bool) (*wtclientrpc.Tower,
	error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	return m.client.GetTowerInfo(rpcCtx,
		&wtclientrpc.GetTowerInfoRequest{
			Pubkey:                   pubkey,
			IncludeSessions:          includeSessions,
			ExcludeExhaustedSessions: excludeExhaustedSessions,
		},
	)
}

// ListTowers gets information about all registered watchtowers. The
// `includeSessions` and `excludeExhaustedSessions` flags serve the same
// function as in the `GetTowerInfo` method.
func (m *wtClient) ListTowers(ctx context.Context, includeSessions,
	excludeExhaustedSessions bool) ([]*wtclientrpc.Tower, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.ListTowers(rpcCtx, &wtclientrpc.ListTowersRequest{
		IncludeSessions:          includeSessions,
		ExcludeExhaustedSessions: excludeExhaustedSessions,
	})
	if err != nil {
		return nil, err
	}

	return resp.Towers, nil
}

// Policy returns the active watchtower client policy configuration.
func (m *wtClient) Policy(ctx context.Context,
	policyType wtclientrpc.PolicyType) (*wtclientrpc.PolicyResponse,
	error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	return m.client.Policy(rpcCtx, &wtclientrpc.PolicyRequest{
		PolicyType: policyType,
	})
}

// RemoveTower removes a watchtower from being considered for future
// session negotiations and from being used for any subsequent backups
// until it's added again. If an address is provided, then this RPC only
// serves as a way of removing the address from the watchtower instead.
func (m *wtClient) RemoveTower(ctx context.Context, pubkey []byte,
	address string) error {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	_, err := m.client.RemoveTower(rpcCtx, &wtclientrpc.RemoveTowerRequest{
		Pubkey:  pubkey,
		Address: address,
	})

	return err
}

// Stats returns the in-memory statistics of the client since startup.
func (m *wtClient) Stats(ctx context.Context) (*wtclientrpc.StatsResponse,
	error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	return m.client.Stats(rpcCtx, &wtclientrpc.StatsRequest{})
}

// TerminateSession terminates the given session and marks it to not be used
// for backups anymore. Returns a human-readable status string.
func (m *wtClient) TerminateSession(ctx context.Context,
	sessionId []byte) (string, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.TerminateSession(rpcCtx,
		&wtclientrpc.TerminateSessionRequest{
			SessionId: sessionId,
		},
	)
	if err != nil {
		return "", err
	}

	return resp.Status, nil
}
