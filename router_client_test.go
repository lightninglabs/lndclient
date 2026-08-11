package lndclient

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/routing/route"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type mockRouteFeeRPCClient struct {
	routerrpc.RouterClient

	request  *routerrpc.RouteFeeRequest
	response *routerrpc.RouteFeeResponse
	err      error
}

func (m *mockRouteFeeRPCClient) EstimateRouteFee(_ context.Context,
	request *routerrpc.RouteFeeRequest, _ ...grpc.CallOption) (
	*routerrpc.RouteFeeResponse, error) {

	m.request = request

	return m.response, m.err
}

// TestEstimateRouteFeeWithProbe checks that invoice-based route fee estimates
// are mapped to lnd's RouteFeeRequest fields and response values.
func TestEstimateRouteFeeWithProbe(t *testing.T) {
	t.Parallel()

	mock := &mockRouteFeeRPCClient{
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
	mock := &mockRouteFeeRPCClient{
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
			mock := &mockRouteFeeRPCClient{}
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

type mockSendPaymentRPCClient struct {
	routerrpc.RouterClient

	request *routerrpc.SendPaymentRequest
}

func (m *mockSendPaymentRPCClient) SendPaymentV2(_ context.Context,
	request *routerrpc.SendPaymentRequest, _ ...grpc.CallOption) (
	routerrpc.Router_SendPaymentV2Client, error) {

	m.request = request

	return &mockSendPaymentStream{}, nil
}

type mockSendPaymentStream struct {
	routerrpc.Router_SendPaymentV2Client
}

func (m *mockSendPaymentStream) Recv() (*lnrpc.Payment, error) {
	return nil, io.EOF
}

// TestSendPaymentComponents checks that all invoice component fields are
// forwarded to lnd without an encoded payment request.
func TestSendPaymentComponents(t *testing.T) {
	t.Parallel()

	target := testVertex()
	paymentHash := lntypes.Hash{1, 2, 3}
	paymentAddr := [32]byte{4, 5, 6}
	destFeatures := []lnrpc.FeatureBit{
		lnrpc.FeatureBit_TLV_ONION_REQ,
		lnrpc.FeatureBit_PAYMENT_ADDR_REQ,
		lnrpc.FeatureBit_MPP_OPT,
	}

	mock := &mockSendPaymentRPCClient{}
	client := &routerClient{
		client: mock,
	}

	_, _, err := client.SendPayment(
		t.Context(), SendPaymentRequest{
			Target:         target,
			AmountMsat:     123456,
			PaymentHash:    &paymentHash,
			PaymentAddr:    &paymentAddr,
			FinalCLTVDelta: 144,
			DestFeatures:   destFeatures,
		},
	)
	require.NoError(t, err)

	require.Empty(t, mock.request.PaymentRequest)
	require.Equal(t, target[:], mock.request.Dest)
	require.Zero(t, mock.request.Amt)
	require.Equal(t, int64(123456), mock.request.AmtMsat)
	require.Equal(t, paymentHash[:], mock.request.PaymentHash)
	require.Equal(t, paymentAddr[:], mock.request.PaymentAddr)
	require.Equal(t, int32(144), mock.request.FinalCltvDelta)
	require.Equal(t, destFeatures, mock.request.DestFeatures)
}

// TestSendPaymentAMPComponents checks that AMP component payments can omit a
// payment hash for lnd to generate the AMP payment identifiers.
func TestSendPaymentAMPComponents(t *testing.T) {
	t.Parallel()

	mock := &mockSendPaymentRPCClient{}
	client := &routerClient{
		client: mock,
	}

	_, _, err := client.SendPayment(
		t.Context(), SendPaymentRequest{
			Target:     testVertex(),
			AmountMsat: 123456,
			AMP:        true,
		},
	)
	require.NoError(t, err)

	require.True(t, mock.request.Amp)
	require.Empty(t, mock.request.PaymentHash)
}

func testVertex() route.Vertex {
	var vertex route.Vertex
	for i := range vertex {
		vertex[i] = byte(i + 1)
	}

	return vertex
}
