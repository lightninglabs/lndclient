package lndclient

import (
	"context"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc/wtclientrpc"
	"google.golang.org/grpc"
)

// Implementation of the watchtower client interface
// https://lightning.engineering/api-docs/category/watchtowerclient-service/
type WatchtowerClientClient interface {
	ServiceClient[wtclientrpc.WatchtowerClientClient]

	AddTower(ctx context.Context, pubkey []byte, address string) error

	// Sets the given tower's status to inactive so that it's not considered
	// for session negotiation. Returns a human readable status string.
	DeactivateTower(ctx context.Context, pubkey []byte) (string, error)

	GetTowerInfo(ctx context.Context, pubkey []byte, includeSessions,
		excludeExhaustedSessions bool) (*wtclientrpc.Tower, error)

	ListTowers(ctx context.Context, includeSessions,
		excludeExhaustedSessions bool) ([]*wtclientrpc.Tower, error)

	Policy(ctx context.Context, policyType wtclientrpc.PolicyType) (*PolicyResponse, error)

	RemoveTower(ctx context.Context, pubkey []byte, address string) error

	Stats(ctx context.Context) (*StatsResponse, error)

	// Terminates the given session and marks it to not be used for backups
	// anymore. Returns a human readable status string.
	TerminateSession(ctx context.Context, sessionId []byte) (string, error)
}

// Response returned by `Policy`
// https://lightning.engineering/api-docs/api/lnd/watchtower-client/policy/#wtclientrpcpolicyresponse
type PolicyResponse struct {
	maxUpdates       uint32
	sweepSatPerVbyte uint32
}

// Response returned by `Stats`
// https://lightning.engineering/api-docs/api/lnd/watchtower-client/stats/#wtclientrpcstatsresponse
type StatsResponse struct {
	numBackups           uint32
	numPendingBackups    uint32
	numFailedBackups     uint32
	numSessionsAcquired  uint32
	numSessionsExhausted uint32
}

type wtClientClient struct {
	client      wtclientrpc.WatchtowerClientClient
	wtClientMac serializedMacaroon
	timeout     time.Duration
}

// A compile time check to ensure that  wtClientClient implements the
// WtclientClient interface.
var _ WatchtowerClientClient = (*wtClientClient)(nil)

// newClientClient creates a new watchtower client interface.
func newWtClientClient(conn grpc.ClientConnInterface,
	wtClientMac serializedMacaroon, timeout time.Duration) *wtClientClient {

	return &wtClientClient{
		client:      wtclientrpc.NewWatchtowerClientClient(conn),
		wtClientMac: wtClientMac,
		timeout:     timeout,
	}
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (m *wtClientClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	wtclientrpc.WatchtowerClientClient) {

	return m.wtClientMac.WithMacaroonAuth(parentCtx), m.timeout, m.client
}

// AddTower adds a new watchtower reachable at the given address and considers
// it for new sessions. If the watchtower already exists, then any new addresses
// included will be considered when dialing it for session negotiations and
// backups.
func (m *wtClientClient) AddTower(ctx context.Context, pubkey []byte, address string) error {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcReq := &wtclientrpc.AddTowerRequest{
		Pubkey:  pubkey,
		Address: address,
	}

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	_, err := m.client.AddTower(rpcCtx, rpcReq)
	if err != nil {
		return err
	}

	return nil
}

// DeactivateTower sets the given tower's status to inactive so that it is not
// considered for session negotiation. Its sessions will also not be used while
// the tower is inactive.
func (m *wtClientClient) DeactivateTower(ctx context.Context, pubkey []byte) (string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.DeactivateTower(rpcCtx, &wtclientrpc.DeactivateTowerRequest{
		Pubkey: pubkey,
	})
	if err != nil {
		return "", err
	}

	return resp.Status, nil
}

// GetTowerInfo gets information about a watchtower that corresponds to the
// given pubkey. The `includeSessions` flag controls whether session information is
// included. The `excludeExhaustedSessions` controls whether exhausted sessions
// are included in the response.
func (m *wtClientClient) GetTowerInfo(ctx context.Context, pubkey []byte, includeSessions,
	excludeExhaustedSessions bool) (*wtclientrpc.Tower, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.GetTowerInfo(rpcCtx, &wtclientrpc.GetTowerInfoRequest{
		Pubkey:                   pubkey,
		IncludeSessions:          includeSessions,
		ExcludeExhaustedSessions: excludeExhaustedSessions,
	})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// ListTowers gets information about all registered watchtowers. The
// `includeSessions` and `excludeExhaustedSessions` flags serve the same function as
// in the `GetTowerInfo` method.
func (m *wtClientClient) ListTowers(ctx context.Context, includeSessions,
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
func (m *wtClientClient) Policy(ctx context.Context, policyType wtclientrpc.PolicyType) (*PolicyResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.Policy(rpcCtx, &wtclientrpc.PolicyRequest{
		PolicyType: policyType,
	})
	if err != nil {
		return nil, err
	}

	return &PolicyResponse{
		maxUpdates:       resp.MaxUpdates,
		sweepSatPerVbyte: resp.SweepSatPerVbyte,
	}, nil
}

// RemoveTower removes a watchtower from being considered for future session
// negotiations and from being used for any subsequent backups until it's added
// again. If an address is provided, then this RPC only serves as a way of
// removing the address from the watchtower instead.
func (m *wtClientClient) RemoveTower(ctx context.Context, pubkey []byte, address string) error {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	_, err := m.client.RemoveTower(rpcCtx, &wtclientrpc.RemoveTowerRequest{
		Pubkey:  pubkey,
		Address: address,
	})
	if err != nil {
		return err
	}

	return nil
}

// Stats returns the in-memory statistics of the client since startup.
func (m *wtClientClient) Stats(ctx context.Context) (*StatsResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.Stats(rpcCtx, &wtclientrpc.StatsRequest{})
	if err != nil {
		return nil, err
	}

	return &StatsResponse{
		numBackups:           resp.NumBackups,
		numPendingBackups:    resp.NumPendingBackups,
		numFailedBackups:     resp.NumFailedBackups,
		numSessionsAcquired:  resp.NumSessionsAcquired,
		numSessionsExhausted: resp.NumSessionsExhausted,
	}, nil
}

// Terminate terminates the given session and marks it as terminal so that it is
// not used for backups anymore.
func (m *wtClientClient) TerminateSession(ctx context.Context, sessionId []byte) (string, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	rpcCtx = m.wtClientMac.WithMacaroonAuth(rpcCtx)
	resp, err := m.client.TerminateSession(rpcCtx, &wtclientrpc.TerminateSessionRequest{
		SessionId: sessionId,
	})
	if err != nil {
		return "", err
	}

	return resp.Status, nil
}
