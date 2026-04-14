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

// fallbackAddr is just a Bitcoin address used for tests of FallbackAddr field.
const fallbackAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080"

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

// assertInvoiceRequestParity verifies the shared fields that should be encoded
// identically by AddInvoice and AddHoldInvoice.
func assertInvoiceRequestParity(t *testing.T, add *lnrpc.Invoice,
	hold *invoicesrpc.AddHoldInvoiceRequest) {

	t.Helper()

	require.Equal(t, add.Memo, hold.Memo)
	require.Equal(t, add.ValueMsat, hold.ValueMsat)
	require.Equal(t, add.DescriptionHash, hold.DescriptionHash)
	require.Equal(t, add.Expiry, hold.Expiry)
	require.Equal(t, add.FallbackAddr, hold.FallbackAddr)
	require.Equal(t, add.CltvExpiry, hold.CltvExpiry)
	require.Equal(t, add.Private, hold.Private)
	require.Equal(t, add.RouteHints, hold.RouteHints)
}

// TestInvoiceClientAddInvoiceParity ensures AddInvoice and AddHoldInvoice
// encode the same explicit invoice fields for the same invoice input.
func TestInvoiceClientAddInvoiceParity(t *testing.T) {
	var validPreimage lntypes.Preimage
	copy(validPreimage[:], "valid preimage")

	var validRHash lntypes.Hash
	copy(validRHash[:], "valid hash")

	sharedInvoice := invoicesrpc.AddInvoiceData{
		Memo:            "fake memo",
		Value:           lnwire.MilliSatoshi(500000),
		DescriptionHash: []byte("fake 32 byte hash"),
		Expiry:          123,
		FallbackAddr:    fallbackAddr,
		CltvExpiry:      456,
		Private:         true,
		RouteHints:      testInvoiceRouteHints(),
	}

	// The two wrappers use different invoice creation RPCs, so we provide
	// path-specific fixtures for their mutually exclusive fields.
	lightningInvoice := sharedInvoice
	lightningInvoice.Preimage = &validPreimage

	holdInvoice := sharedInvoice
	holdInvoice.Hash = &validRHash

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

	_, _, err := lightning.AddInvoice(t.Context(), &lightningInvoice)
	require.NoError(t, err)

	_, err = invoices.AddHoldInvoice(t.Context(), &holdInvoice)
	require.NoError(t, err)

	require.Len(t, lightningRPC.addInvoiceArgs, 1)
	require.Len(t, holdRPC.addHoldInvoiceArgs, 1)

	assertInvoiceRequestParity(
		t, lightningRPC.addInvoiceArgs[0].in,
		holdRPC.addHoldInvoiceArgs[0].in,
	)
}

// TestInvoicesClientAddHoldInvoiceIgnoresUnsupportedFields ensures the
// AddHoldInvoice wrapper still forwards the supported request fields when AMP
// or blinded-path-only inputs are provided.
func TestInvoicesClientAddHoldInvoiceIgnoresUnsupportedFields(t *testing.T) {
	var validRHash lntypes.Hash
	copy(validRHash[:], "valid hash")

	invoice := &invoicesrpc.AddInvoiceData{
		Memo:            "fake memo",
		Hash:            &validRHash,
		Value:           lnwire.MilliSatoshi(500000),
		DescriptionHash: []byte("fake 32 byte hash"),
		Expiry:          123,
		FallbackAddr:    fallbackAddr,
		CltvExpiry:      456,
		Private:         true,
		Amp:             true,
		BlindedPathCfg: &invoicesrpc.BlindedPathConfig{
			MinNumPathHops: 5,
		},
		RouteHints: testInvoiceRouteHints(),
	}

	rpcRouteHints := testRPCRouteHints(t)
	expectedRequest := &invoicesrpc.AddHoldInvoiceRequest{
		Memo:            invoice.Memo,
		Hash:            invoice.Hash[:],
		ValueMsat:       int64(invoice.Value),
		DescriptionHash: invoice.DescriptionHash,
		Expiry:          invoice.Expiry,
		FallbackAddr:    invoice.FallbackAddr,
		CltvExpiry:      invoice.CltvExpiry,
		Private:         invoice.Private,
		RouteHints:      rpcRouteHints,
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

	invoices := &invoicesClient{
		client: holdRPC,
	}

	_, err := invoices.AddHoldInvoice(t.Context(), invoice)
	require.NoError(t, err)

	require.Len(t, holdRPC.addHoldInvoiceArgs, 1)
	require.Equal(t, expectedRequest, holdRPC.addHoldInvoiceArgs[0].in)
}
