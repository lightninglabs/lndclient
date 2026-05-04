package lndclient

import (
	"context"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/routing/route"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type mockRouterRPCClient struct {
	routerrpc.RouterClient

	request  *routerrpc.RouteFeeRequest
	response *routerrpc.RouteFeeResponse
	err      error
}

func (m *mockRouterRPCClient) EstimateRouteFee(_ context.Context,
	request *routerrpc.RouteFeeRequest, _ ...grpc.CallOption) (
	*routerrpc.RouteFeeResponse, error) {

	m.request = request
	return m.response, m.err
}

// TestEstimateRouteFeeWithProbe checks that invoice-based route fee estimates
// are mapped to lnd's RouteFeeRequest fields and response values.
func TestEstimateRouteFeeWithProbe(t *testing.T) {
	t.Parallel()

	mock := &mockRouterRPCClient{
		response: &routerrpc.RouteFeeResponse{
			RoutingFeeMsat: 987,
			TimeLockDelay:  654,
			FailureReason:  lnrpc.PaymentFailureReason_FAILURE_REASON_NONE,
		},
	}
	client := &routerClient{
		client: mock,
	}

	resp, err := client.EstimateRouteFeeWithProbe(
		t.Context(), "lnbc1...", 1500*time.Millisecond,
	)
	require.NoError(t, err)

	require.Empty(t, mock.request.Dest)
	require.Zero(t, mock.request.AmtSat)
	require.Equal(t, "lnbc1...", mock.request.PaymentRequest)
	require.Equal(t, uint32(2), mock.request.Timeout)
	require.Equal(t, lnwire.MilliSatoshi(987), resp.RoutingFee)
	require.Equal(t, int64(654), resp.TimeLockDelay)
	require.Equal(
		t, lnrpc.PaymentFailureReason_FAILURE_REASON_NONE,
		resp.FailureReason,
	)
}

// TestEstimateRouteFee checks that destination and amount route fee estimates
// are mapped to lnd's RouteFeeRequest fields.
func TestEstimateRouteFee(t *testing.T) {
	t.Parallel()

	dest := testVertex()
	mock := &mockRouterRPCClient{
		response: &routerrpc.RouteFeeResponse{
			RoutingFeeMsat: 4321,
		},
	}
	client := &routerClient{
		client: mock,
	}

	fee, err := client.EstimateRouteFee(
		t.Context(), dest, btcutil.Amount(1000),
	)
	require.NoError(t, err)

	require.Equal(t, dest[:], mock.request.Dest)
	require.Equal(t, int64(1000), mock.request.AmtSat)
	require.Equal(t, lnwire.MilliSatoshi(4321), fee)
}

// TestEstimateRouteFeeWithProbeRejectsInvalidTimeout checks that invalid probe
// timeout values are rejected before making the RPC.
func TestEstimateRouteFeeWithProbeRejectsInvalidTimeout(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		timeout time.Duration
		err     string
	}{
		{
			name:    "negative",
			timeout: -time.Second,
			err:     "timeout must not be negative",
		},
		{
			name: "too large",
			timeout: time.Duration(^uint32(0))*time.Second +
				time.Nanosecond,
			err: "timeout exceeds maximum",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockRouterRPCClient{}
			client := &routerClient{
				client: mock,
			}

			_, err := client.EstimateRouteFeeWithProbe(
				t.Context(), "lnbc1...", tc.timeout,
			)
			require.ErrorContains(t, err, tc.err)
			require.Nil(t, mock.request)
		})
	}
}

func testVertex() route.Vertex {
	var vertex route.Vertex
	for i := range vertex {
		vertex[i] = byte(i + 1)
	}

	return vertex
}
