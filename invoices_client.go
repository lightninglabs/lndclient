package lndclient

import (
	"context"
	"errors"
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

type InvoiceHtlcModifyRequest struct {
	Invoice *lnrpc.Invoice

	CircuitKey invpkg.CircuitKey

	ExitHtlcAmt lnwire.MilliSatoshi

	ExitHtlcExpiry uint32

	CurrentHeight uint32

	WireCustomRecords lnwire.CustomRecords
}

type InvoiceHtlcModifyResponse struct {
	CircuitKey invpkg.CircuitKey

	AmtPaid lnwire.MilliSatoshi
}

type InvoiceHtlcModifyHandler func(context.Context,
	InvoiceHtlcModifyRequest) (*InvoiceHtlcModifyResponse, error)

// InvoicesClient exposes invoice functionality.
type InvoicesClient interface {
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
	State   invpkg.ContractState
	AmtPaid btcutil.Amount
}

type invoicesClient struct {
	client     invoicesrpc.InvoicesClient
	invoiceMac serializedMacaroon
	timeout    time.Duration
	quitOnce   sync.Once
	quit       chan struct{}
	wg         sync.WaitGroup
}

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
				State:   state,
				AmtPaid: btcutil.Amount(invoice.AmtPaidSat),
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

	rpcIn := &invoicesrpc.AddHoldInvoiceRequest{
		Memo:       in.Memo,
		Hash:       in.Hash[:],
		Value:      int64(in.Value.ToSatoshis()),
		Expiry:     in.Expiry,
		CltvExpiry: in.CltvExpiry,
		Private:    true,
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
				rpcResp := &invoicesrpc.HtlcModifyResponse{
					CircuitKey: &invoicesrpc.CircuitKey{
						ChanId: key.ChanID.ToUint64(),
						HtlcId: key.HtlcID,
					},
					AmtPaid: uint64(resp.AmtPaid),
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
