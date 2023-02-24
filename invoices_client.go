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
	"google.golang.org/grpc"
)

// InvoicesClient exposes invoice functionality.
type InvoicesClient interface {
	SubscribeSingleInvoice(ctx context.Context, hash lntypes.Hash) (
		<-chan InvoiceUpdate, <-chan error, error)

	SettleInvoice(ctx context.Context, preimage lntypes.Preimage) error

	CancelInvoice(ctx context.Context, hash lntypes.Hash) error

	AddHoldInvoice(ctx context.Context, in *invoicesrpc.AddInvoiceData) (
		string, error)
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
	wg         sync.WaitGroup
}

func newInvoicesClient(conn grpc.ClientConnInterface,
	invoiceMac serializedMacaroon, timeout time.Duration) *invoicesClient {

	return &invoicesClient{
		client:     invoicesrpc.NewInvoicesClient(conn),
		invoiceMac: invoiceMac,
		timeout:    timeout,
	}
}

func (s *invoicesClient) WaitForFinished() {
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
