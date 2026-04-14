package lndclient

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/zpay32"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// testInvoiceRouteHints returns deterministic route hints for invoice tests.
func testInvoiceRouteHints() [][]zpay32.HopHint {
	_, pubKey1 := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{1}, 32))
	_, pubKey2 := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{2}, 32))
	_, pubKey3 := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{3}, 32))

	return [][]zpay32.HopHint{
		{
			{
				NodeID:                    pubKey1,
				ChannelID:                 101,
				FeeBaseMSat:               1001,
				FeeProportionalMillionths: 2001,
				CLTVExpiryDelta:           40,
			},
			{
				NodeID:                    pubKey2,
				ChannelID:                 102,
				FeeBaseMSat:               1002,
				FeeProportionalMillionths: 2002,
				CLTVExpiryDelta:           41,
			},
		},
		{
			{
				NodeID:                    pubKey3,
				ChannelID:                 103,
				FeeBaseMSat:               1003,
				FeeProportionalMillionths: 2003,
				CLTVExpiryDelta:           42,
			},
		},
	}
}

// testRPCRouteHints returns the RPC form of the deterministic route hints.
func testRPCRouteHints(t *testing.T) []*lnrpc.RouteHint {
	t.Helper()

	rpcRouteHints, err := marshallRouteHints(testInvoiceRouteHints())
	require.NoError(t, err)

	return rpcRouteHints
}

// addHoldInvoiceArg records the args used in
// mockInvoicesRPCClient.AddHoldInvoice.
type addHoldInvoiceArg struct {
	in   *invoicesrpc.AddHoldInvoiceRequest
	opts []grpc.CallOption
}

// mockInvoicesRPCClient implements invoicesrpc.InvoicesClient with a dynamic
// AddHoldInvoice implementation and call spying.
type mockInvoicesRPCClient struct {
	invoicesrpc.InvoicesClient

	addHoldInvoice func(in *invoicesrpc.AddHoldInvoiceRequest,
		opts ...grpc.CallOption) (*invoicesrpc.AddHoldInvoiceResp,
		error)

	addHoldInvoiceArgs []addHoldInvoiceArg
}

// AddHoldInvoice records the call and forwards it to the test hook.
func (m *mockInvoicesRPCClient) AddHoldInvoice(ctx context.Context,
	in *invoicesrpc.AddHoldInvoiceRequest,
	opts ...grpc.CallOption) (*invoicesrpc.AddHoldInvoiceResp, error) {

	m.addHoldInvoiceArgs = append(m.addHoldInvoiceArgs, addHoldInvoiceArg{
		in:   in,
		opts: opts,
	})

	return m.addHoldInvoice(in, opts...)
}

// TestInvoiceClientAddInvoiceRouteHintParity ensures AddInvoice and
// AddHoldInvoice encode the same route hints for the same invoice input.
func TestInvoiceClientAddInvoiceRouteHintParity(t *testing.T) {
	var validPreimage lntypes.Preimage
	copy(validPreimage[:], "valid preimage")

	var validRHash lntypes.Hash
	copy(validRHash[:], "valid hash")

	invoice := &invoicesrpc.AddInvoiceData{
		Memo:            "fake memo",
		Preimage:        &validPreimage,
		Hash:            &validRHash,
		Value:           lnwire.MilliSatoshi(500000),
		DescriptionHash: []byte("fake 32 byte hash"),
		Expiry:          123,
		CltvExpiry:      456,
		RouteHints:      testInvoiceRouteHints(),
	}

	lightningRPC := &mockRPCClient{
		addInvoice: func(_ *lnrpc.Invoice,
			_ ...grpc.CallOption) (*lnrpc.AddInvoiceResponse,
			error) {

			return &lnrpc.AddInvoiceResponse{
				RHash:          validRHash[:],
				PaymentRequest: "swap invoice",
			}, nil
		},
	}
	holdRPC := &mockInvoicesRPCClient{
		addHoldInvoice: func(_ *invoicesrpc.AddHoldInvoiceRequest,
			_ ...grpc.CallOption) (*invoicesrpc.AddHoldInvoiceResp,
			error) {

			return &invoicesrpc.AddHoldInvoiceResp{
				PaymentRequest: "probe invoice",
			}, nil
		},
	}

	lightning := &lightningClient{
		client: lightningRPC,
	}
	invoices := &invoicesClient{
		client: holdRPC,
	}

	_, _, err := lightning.AddInvoice(t.Context(), invoice)
	require.NoError(t, err)

	_, err = invoices.AddHoldInvoice(t.Context(), invoice)
	require.NoError(t, err)

	require.Len(t, lightningRPC.addInvoiceArgs, 1)
	require.Len(t, holdRPC.addHoldInvoiceArgs, 1)

	require.Equal(
		t, holdRPC.addHoldInvoiceArgs[0].in.RouteHints,
		lightningRPC.addInvoiceArgs[0].in.RouteHints,
	)
}
