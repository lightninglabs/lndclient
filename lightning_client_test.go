package lndclient

import (
	"context"
	"errors"
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// addInvoiceArg records the args used in a call to mockRPCClient.AddInvoice.
type addInvoiceArg struct {
	in   *lnrpc.Invoice
	opts []grpc.CallOption
}

// mockRPCClient implements lnrpc.LightningClient with dynamic method
// implementations and call spying.
type mockRPCClient struct {
	lnrpc.LightningClient

	addInvoice func(in *lnrpc.Invoice, opts ...grpc.CallOption) (
		*lnrpc.AddInvoiceResponse, error)
	addInvoiceArgs []addInvoiceArg
}

func (m *mockRPCClient) AddInvoice(ctx context.Context, in *lnrpc.Invoice,
	opts ...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error) {

	m.addInvoiceArgs = append(m.addInvoiceArgs, addInvoiceArg{
		in:   in,
		opts: opts,
	})

	return m.addInvoice(in, opts...)
}

// assertAddInvoiceArgs verifies the recorded AddInvoice RPC calls.
func assertAddInvoiceArgs(t *testing.T, want, got []addInvoiceArg) {
	t.Helper()

	require.Len(t, got, len(want))

	for i := range want {
		require.Equal(t, want[i].opts, got[i].opts)
		require.Equal(t, want[i].in.Memo, got[i].in.Memo)
		require.Equal(t, want[i].in.RPreimage, got[i].in.RPreimage)
		require.Equal(t, want[i].in.RHash, got[i].in.RHash)
		require.Equal(t, want[i].in.ValueMsat, got[i].in.ValueMsat)
		require.Equal(
			t, want[i].in.DescriptionHash,
			got[i].in.DescriptionHash,
		)
		require.Equal(t, want[i].in.Expiry, got[i].in.Expiry)
		require.Equal(
			t, want[i].in.FallbackAddr, got[i].in.FallbackAddr,
		)
		require.Equal(
			t, want[i].in.CltvExpiry, got[i].in.CltvExpiry,
		)
		require.Equal(t, want[i].in.Private, got[i].in.Private)
		require.Equal(t, want[i].in.IsAmp, got[i].in.IsAmp)
		require.Equal(t, want[i].in.IsBlinded, got[i].in.IsBlinded)

		if want[i].in.BlindedPathConfig == nil {
			require.Nil(t, got[i].in.BlindedPathConfig)
		} else {
			require.Equal(
				t, want[i].in.BlindedPathConfig,
				got[i].in.BlindedPathConfig,
			)
		}

		if len(want[i].in.RouteHints) == 0 {
			require.Empty(t, got[i].in.RouteHints)
			continue
		}

		require.Equal(t, want[i].in.RouteHints, got[i].in.RouteHints)
	}
}

// TestLightningClientAddInvoice ensures that adding an invoice via
// lightningClient is completed as expected.
func TestLightningClientAddInvoice(t *testing.T) {
	// Define constants / fixtures.
	var validPreimage lntypes.Preimage
	copy(validPreimage[:], "valid preimage")
	var validRHash lntypes.Hash
	copy(validRHash[:], "valid hash")
	validRouteHints := testInvoiceRouteHints()
	validRPCRouteHints := testRPCRouteHints(t)

	validAddInvoiceData := &invoicesrpc.AddInvoiceData{
		Memo:            "fake memo",
		Preimage:        &validPreimage,
		Value:           lnwire.MilliSatoshi(500000),
		DescriptionHash: []byte("fake 32 byte hash"),
		Expiry:          123,
		CltvExpiry:      456,
	}

	validInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
	}

	validPayReq := "a valid pay req"
	validResp := &lnrpc.AddInvoiceResponse{
		RHash:          validRHash[:],
		PaymentRequest: validPayReq,
	}

	validAddInvoiceArgs := []addInvoiceArg{
		{in: validInvoice},
	}

	validAddInvoice := func(in *lnrpc.Invoice, opts ...grpc.CallOption) (
		*lnrpc.AddInvoiceResponse, error) {

		return validResp, nil
	}

	privateAddInvoiceData := *validAddInvoiceData
	privateAddInvoiceData.Private = true
	privateInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		Private:         true,
	}
	privateAddInvoiceArgs := []addInvoiceArg{
		{in: privateInvoice},
	}

	fallbackAddrAddInvoiceData := *validAddInvoiceData
	fallbackAddrAddInvoiceData.FallbackAddr = fallbackAddr
	fallbackAddrInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		FallbackAddr:    fallbackAddrAddInvoiceData.FallbackAddr,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
	}
	fallbackAddrAddInvoiceArgs := []addInvoiceArg{
		{in: fallbackAddrInvoice},
	}

	ampAddInvoiceData := &invoicesrpc.AddInvoiceData{
		Memo:            validAddInvoiceData.Memo,
		Value:           validAddInvoiceData.Value,
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		Amp:             true,
	}
	ampInvoice := &lnrpc.Invoice{
		Memo:            ampAddInvoiceData.Memo,
		ValueMsat:       int64(ampAddInvoiceData.Value),
		DescriptionHash: ampAddInvoiceData.DescriptionHash,
		Expiry:          ampAddInvoiceData.Expiry,
		CltvExpiry:      ampAddInvoiceData.CltvExpiry,
		IsAmp:           true,
	}
	ampAddInvoiceArgs := []addInvoiceArg{
		{in: ampInvoice},
	}

	hashAddInvoiceData := *validAddInvoiceData
	hashAddInvoiceData.Preimage = nil
	hashAddInvoiceData.Hash = &validRHash
	hashAddInvoiceData.HodlInvoice = true
	hashInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
	}
	hashAddInvoiceArgs := []addInvoiceArg{
		{in: hashInvoice},
	}

	blindedAddInvoiceData := *validAddInvoiceData
	blindedAddInvoiceData.BlindedPathCfg = &invoicesrpc.BlindedPathConfig{
		MinNumPathHops: 5,
	}
	numHops := uint32(blindedAddInvoiceData.BlindedPathCfg.MinNumPathHops)
	blindedInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		IsBlinded:       true,
		BlindedPathConfig: &lnrpc.BlindedPathConfig{
			NumHops: &numHops,
		},
	}
	blindedAddInvoiceArgs := []addInvoiceArg{
		{in: blindedInvoice},
	}

	blindedZeroHopAddInvoiceData := *validAddInvoiceData
	blindedZeroHopAddInvoiceData.BlindedPathCfg = &invoicesrpc.BlindedPathConfig{}
	blindedZeroHopInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		IsBlinded:       true,
	}
	blindedZeroHopAddInvoiceArgs := []addInvoiceArg{
		{in: blindedZeroHopInvoice},
	}

	routeHintAddInvoiceData := *validAddInvoiceData
	routeHintAddInvoiceData.RouteHints = validRouteHints
	routeHintInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		RouteHints:      validRPCRouteHints,
	}
	routeHintAddInvoiceArgs := []addInvoiceArg{
		{in: routeHintInvoice},
	}

	errorAddInvoice := func(in *lnrpc.Invoice, opts ...grpc.CallOption) (
		*lnrpc.AddInvoiceResponse, error) {

		return nil, errors.New("error")
	}

	// Set up the test structure.
	type expect struct {
		addInvoiceArgs []addInvoiceArg
		hash           lntypes.Hash
		payRequest     string
		wantErr        bool
	}

	type testCase struct {
		name    string
		client  mockRPCClient
		invoice *invoicesrpc.AddInvoiceData
		expect  expect
	}

	// Run through the test cases.
	tests := []testCase{
		{
			name: "happy path",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: validAddInvoiceData,
			expect: expect{
				addInvoiceArgs: validAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "private invoice",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &privateAddInvoiceData,
			expect: expect{
				addInvoiceArgs: privateAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "invoice with fallback address",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &fallbackAddrAddInvoiceData,
			expect: expect{
				addInvoiceArgs: fallbackAddrAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "amp invoice",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: ampAddInvoiceData,
			expect: expect{
				addInvoiceArgs: ampAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "invoice with hash uses standard invoice path",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &hashAddInvoiceData,
			expect: expect{
				addInvoiceArgs: hashAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "blinded invoice",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &blindedAddInvoiceData,
			expect: expect{
				addInvoiceArgs: blindedAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "blinded invoice with zero min path hops",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &blindedZeroHopAddInvoiceData,
			expect: expect{
				addInvoiceArgs: blindedZeroHopAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "invoice with route hints",
			client: mockRPCClient{
				addInvoice: validAddInvoice,
			},
			invoice: &routeHintAddInvoiceData,
			expect: expect{
				addInvoiceArgs: routeHintAddInvoiceArgs,
				hash:           validRHash,
				payRequest:     validPayReq,
			},
		},
		{
			name: "rpc client error",
			client: mockRPCClient{
				addInvoice: errorAddInvoice,
			},
			invoice: validAddInvoiceData,
			expect: expect{
				addInvoiceArgs: validAddInvoiceArgs,
				wantErr:        true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ln := lightningClient{
				client: &test.client,
			}

			hash, payRequest, err := ln.AddInvoice(
				t.Context(), test.invoice,
			)

			// Check if an error (or no error) was received as
			// expected.
			if test.expect.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Check if the expected hash was returned.
			require.Equal(
				t, hash, test.expect.hash,
				"received unexpected hash",
			)

			// Check if the expected invoice was returned.
			require.Equal(
				t, payRequest, test.expect.payRequest,
				"received unexpected payment request",
			)

			// Check if the expected args were passed to the RPC
			// client call.
			assertAddInvoiceArgs(
				t, test.expect.addInvoiceArgs,
				test.client.addInvoiceArgs,
			)
		})
	}
}
