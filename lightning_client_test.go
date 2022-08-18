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

// TestLightningClientAddInvoice ensures that adding an invoice via
// lightningClient is completed as expected.
func TestLightningClientAddInvoice(t *testing.T) {
	// Define constants / fixtures.
	var validPreimage lntypes.Preimage
	copy(validPreimage[:], "valid preimage")
	var validRHash lntypes.Hash
	copy(validRHash[:], "valid hash")
	validAddInvoiceData := &invoicesrpc.AddInvoiceData{
		Memo:            "fake memo",
		Preimage:        &validPreimage,
		Hash:            &validRHash,
		Value:           lnwire.MilliSatoshi(500000),
		DescriptionHash: []byte("fake 32 byte hash"),
		Expiry:          123,
		CltvExpiry:      456,
	}

	validInvoice := &lnrpc.Invoice{
		Memo:            validAddInvoiceData.Memo,
		RPreimage:       validAddInvoiceData.Preimage[:],
		RHash:           validAddInvoiceData.Hash[:],
		ValueMsat:       int64(validAddInvoiceData.Value),
		DescriptionHash: validAddInvoiceData.DescriptionHash,
		Expiry:          validAddInvoiceData.Expiry,
		CltvExpiry:      validAddInvoiceData.CltvExpiry,
		Private:         true,
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
		}, {
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
		test := test
		t.Run(test.name, func(t *testing.T) {
			ln := lightningClient{
				client: &test.client,
			}

			hash, payRequest, err := ln.AddInvoice(
				context.Background(), test.invoice,
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
			require.Equal(t, test.client.addInvoiceArgs,
				test.expect.addInvoiceArgs,
				"rpc client call was not made as expected",
			)
		})
	}
}
