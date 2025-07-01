package lndclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	invpkg "github.com/lightningnetwork/lnd/invoices"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"google.golang.org/grpc"
)

// InvoiceHtlcModifyRequest is a request to modify an HTLC that is attempting to
// settle an invoice.
type InvoiceHtlcModifyRequest struct {
	// Invoice is the current state of the invoice, _before_ this HTLC is
	// applied. Any HTLC in the invoice is a previously accepted/settled
	// one.
	Invoice *lnrpc.Invoice

	// CircuitKey is the circuit key of the HTLC that is attempting to
	// settle the invoice.
	CircuitKey invpkg.CircuitKey

	// ExitHtlcAmt is the amount of the HTLC that is attempting to settle
	// the invoice.
	ExitHtlcAmt lnwire.MilliSatoshi

	// ExitHtlcExpiry is the expiry of the HTLC that is attempting to settle
	// the invoice.
	ExitHtlcExpiry uint32

	// CurrentHeight is the current block height.
	CurrentHeight uint32

	// WireCustomRecords is the wire custom records of the HTLC that is
	// attempting to settle the invoice.
	WireCustomRecords lnwire.CustomRecords
}

// InvoiceHtlcModifyResponse is a response to an HTLC modification request.
type InvoiceHtlcModifyResponse struct {
	// CircuitKey is the circuit key the response is for.
	CircuitKey invpkg.CircuitKey

	// AmtPaid is the amount the HTLC contributes toward settling the
	// invoice. This amount can be different from the on-chain amount of the
	// HTLC in case of custom channels. To not modify the amount and use the
	// on-chain amount, set this to 0.
	AmtPaid lnwire.MilliSatoshi

	// CancelSet is a flag that indicates whether the HTLCs associated with
	// the invoice should get cancelled.
	CancelSet bool
}

// InvoiceHtlcModifyHandler is a function that handles an HTLC modification
// request.
type InvoiceHtlcModifyHandler func(context.Context,
	InvoiceHtlcModifyRequest) (*InvoiceHtlcModifyResponse, error)

// InvoicesClient exposes invoice functionality.
type InvoicesClient interface {
	ServiceClient[invoicesrpc.InvoicesClient]

	SubscribeSingleInvoice(ctx context.Context, hash lntypes.Hash) (
		<-chan InvoiceUpdate, <-chan error, error)

	SettleInvoice(ctx context.Context, preimage lntypes.Preimage) error

	CancelInvoice(ctx context.Context, hash lntypes.Hash) error

	AddHoldInvoice(ctx context.Context, in *invoicesrpc.AddInvoiceData) (
		string, error)

	// HtlcModifier is a bidirectional streaming RPC that allows a client to
	// intercept and modify the HTLCs that attempt to settle the given
	// invoice. The server will send HTLCs of invoices to the client and the
	// client can modify some aspects of the HTLC in order to pass the
	// invoice acceptance tests.
	HtlcModifier(ctx context.Context,
		handler InvoiceHtlcModifyHandler) error
}

// InvoiceUpdate contains a state update for an invoice.
type InvoiceUpdate struct {
	State       invpkg.ContractState
	AmtPaid     btcutil.Amount
	AmtPaidMsat lnwire.MilliSatoshi
}

type invoicesClient struct {
	client     invoicesrpc.InvoicesClient
	invoiceMac serializedMacaroon
	timeout    time.Duration
	quitOnce   sync.Once
	quit       chan struct{}
	wg         sync.WaitGroup
}

// A compile time check to ensure that invoicesClient implements the
// InvoicesClient interface.
var _ InvoicesClient = (*invoicesClient)(nil)

func newInvoicesClient(conn grpc.ClientConnInterface,
	invoiceMac serializedMacaroon, timeout time.Duration) *invoicesClient {

	return &invoicesClient{
		client:     invoicesrpc.NewInvoicesClient(conn),
		invoiceMac: invoiceMac,
		timeout:    timeout,
		quit:       make(chan struct{}),
	}
}

func (s *invoicesClient) WaitForFinished() {
	s.quitOnce.Do(func() {
		close(s.quit)
	})

	s.wg.Wait()
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (s *invoicesClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	invoicesrpc.InvoicesClient) {

	return s.invoiceMac.WithMacaroonAuth(parentCtx), s.timeout, s.client
}

func (s *invoicesClient) SettleInvoice(ctx context.Context,
	preimage lntypes.Preimage) error {

	timeoutCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx := s.invoiceMac.WithMacaroonAuth(timeoutCtx)
	_, err := s.client.SettleInvoice(rpcCtx, &invoicesrpc.SettleInvoiceMsg{
		Preimage: preimage[:],
	})

	return err
}

func (s *invoicesClient) CancelInvoice(ctx context.Context,
	hash lntypes.Hash) error {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	rpcCtx = s.invoiceMac.WithMacaroonAuth(rpcCtx)
	_, err := s.client.CancelInvoice(rpcCtx, &invoicesrpc.CancelInvoiceMsg{
		PaymentHash: hash[:],
	})

	return err
}

func (s *invoicesClient) SubscribeSingleInvoice(ctx context.Context,
	hash lntypes.Hash) (<-chan InvoiceUpdate,
	<-chan error, error) {

	invoiceStream, err := s.client.SubscribeSingleInvoice(
		s.invoiceMac.WithMacaroonAuth(ctx),
		&invoicesrpc.SubscribeSingleInvoiceRequest{
			RHash: hash[:],
		},
	)
	if err != nil {
		return nil, nil, err
	}

	updateChan := make(chan InvoiceUpdate)
	errChan := make(chan error, 1)

	// Invoice updates goroutine.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			invoice, err := invoiceStream.Recv()
			if err != nil {
				// If we get an EOF error, the invoice has
				// reached a final state and the server is
				// finished sending us updates. We close both
				// channels to signal that we are done sending
				// values on them and return.
				if err == io.EOF {
					close(updateChan)
					close(errChan)
					return
				}

				errChan <- err
				return
			}

			state, err := fromRPCInvoiceState(invoice.State)
			if err != nil {
				errChan <- err
				return
			}

			select {
			case updateChan <- InvoiceUpdate{
				State:       state,
				AmtPaid:     btcutil.Amount(invoice.AmtPaidSat),
				AmtPaidMsat: lnwire.MilliSatoshi(invoice.AmtPaidMsat),
			}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return updateChan, errChan, nil
}

func (s *invoicesClient) AddHoldInvoice(ctx context.Context,
	in *invoicesrpc.AddInvoiceData) (string, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	routeHints, err := marshallRouteHints(in.RouteHints)
	if err != nil {
		return "", fmt.Errorf("failed to marshal route hints: %v", err)
	}

	rpcIn := &invoicesrpc.AddHoldInvoiceRequest{
		Memo:            in.Memo,
		Hash:            in.Hash[:],
		ValueMsat:       int64(in.Value),
		Expiry:          in.Expiry,
		CltvExpiry:      in.CltvExpiry,
		Private:         in.Private,
		RouteHints:      routeHints,
		DescriptionHash: in.DescriptionHash,
		FallbackAddr:    in.FallbackAddr,
	}

	rpcCtx = s.invoiceMac.WithMacaroonAuth(rpcCtx)
	resp, err := s.client.AddHoldInvoice(rpcCtx, rpcIn)
	if err != nil {
		return "", err
	}
	return resp.PaymentRequest, nil
}

func fromRPCInvoiceState(state lnrpc.Invoice_InvoiceState) (
	invpkg.ContractState, error) {

	switch state {
	case lnrpc.Invoice_OPEN:
		return invpkg.ContractOpen, nil

	case lnrpc.Invoice_ACCEPTED:
		return invpkg.ContractAccepted, nil

	case lnrpc.Invoice_SETTLED:
		return invpkg.ContractSettled, nil

	case lnrpc.Invoice_CANCELED:
		return invpkg.ContractCanceled, nil
	}

	return 0, errors.New("unknown state")
}

// HtlcModifier is a bidirectional streaming RPC that allows a client to
// intercept and modify the HTLCs that attempt to settle the given invoice. The
// server will send HTLCs of invoices to the client and the client can modify
// some aspects of the HTLC in order to pass the invoice acceptance tests.
func (s *invoicesClient) HtlcModifier(ctx context.Context,
	handler InvoiceHtlcModifyHandler) error {

	// Create a child context that will be canceled when this function
	// exits. We use this context to be able to cancel goroutines when we
	// exit on errors, because the parent context won't be canceled in that
	// case.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := s.client.HtlcModifier(
		s.invoiceMac.WithMacaroonAuth(ctx),
	)
	if err != nil {
		return err
	}

	// Create an error channel that we'll send errors on if any of our
	// goroutines fail. We buffer by 1 so that the goroutine doesn't depend
	// on the stream being read, and select on context cancellation and
	// quit channel so that we do not block in the case where we exit with
	// multiple errors.
	errChan := make(chan error, 1)

	sendErr := func(err error) {
		select {
		case errChan <- err:
		case <-ctx.Done():
		case <-s.quit:
		}
	}

	// Start a goroutine that consumes interception requests from lnd and
	// sends them into our requests channel for handling. The requests
	// channel is not buffered because we expect all requests to be handled
	// until this function exits, at which point we expect our context to
	// be canceled or quit channel to be closed.
	requestChan := make(chan InvoiceHtlcModifyRequest)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			// Do a quick check whether our client context has been
			// canceled so that we can exit sooner if needed.
			if ctx.Err() != nil {
				return
			}

			req, err := stream.Recv()
			if err != nil {
				sendErr(err)
				return
			}

			wireCustomRecords := req.ExitHtlcWireCustomRecords
			interceptReq := InvoiceHtlcModifyRequest{
				Invoice: req.Invoice,
				CircuitKey: invpkg.CircuitKey{
					ChanID: lnwire.NewShortChanIDFromInt(
						req.ExitHtlcCircuitKey.ChanId,
					),
					HtlcID: req.ExitHtlcCircuitKey.HtlcId,
				},
				ExitHtlcAmt: lnwire.MilliSatoshi(
					req.ExitHtlcAmt,
				),
				ExitHtlcExpiry:    req.ExitHtlcExpiry,
				CurrentHeight:     req.CurrentHeight,
				WireCustomRecords: wireCustomRecords,
			}

			// Try to send our interception request, failing on
			// context cancel or router exit.
			select {
			case requestChan <- interceptReq:

			case <-s.quit:
				sendErr(ErrRouterShuttingDown)
				return

			case <-ctx.Done():
				sendErr(ctx.Err())
				return
			}
		}
	}()

	for {
		select {
		case request := <-requestChan:
			// Handle requests in a goroutine so that the handler
			// provided to this function can be blocking. If we
			// get an error, send it into our error channel to
			// shut down the interceptor.
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()

				// Get a response from handler, this may block
				// for a while.
				resp, err := handler(ctx, request)
				if err != nil {
					sendErr(err)
					return
				}

				key := resp.CircuitKey
				amtPaid := uint64(resp.AmtPaid)
				rpcResp := &invoicesrpc.HtlcModifyResponse{
					CircuitKey: &invoicesrpc.CircuitKey{
						ChanId: key.ChanID.ToUint64(),
						HtlcId: key.HtlcID,
					},
					AmtPaid:   &amtPaid,
					CancelSet: resp.CancelSet,
				}

				if err := stream.Send(rpcResp); err != nil {
					sendErr(err)
					return
				}
			}()

		// If one of our goroutines fails, exit with the error that
		// occurred.
		case err := <-errChan:
			return err

		case <-s.quit:
			return ErrRouterShuttingDown

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
