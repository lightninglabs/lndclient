package lndclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"google.golang.org/grpc"
)

// NotifierOptions is a set of functional options that allow callers to further
// modify the type of chain even notifications they receive.
type NotifierOptions struct {
	// IncludeBlock if true, then the dispatched confirmation notification
	// will include the block that mined the transaction.
	IncludeBlock bool

	// ReOrgChan if set, will be sent on if the transaction is re-organized
	// out of the chain. This channel being set will also imply that we
	// don't cancel the notification listener after having received one
	// confirmation event. That means the caller manually needs to cancel
	// the passed in context to cancel being notified once the required
	// number of confirmations have been reached.
	ReOrgChan chan struct{}

	// ReOrgDepthChan if set, will be sent the depth of a re-org whenever
	// the transaction is re-organized out of the chain. It carries the same
	// signal as ReOrgChan but with the re-org depth attached. Only
	// confirmation notifications populate a depth; spend notifications send
	// 0. Like ReOrgChan, setting this keeps the notification listener alive
	// past the first event.
	ReOrgDepthChan chan int32

	// DoneChan if set, will be sent on once the notification is no longer
	// under the risk of being re-organized out of the chain, i.e. it has
	// reached the backend's re-org safety depth. Setting this keeps the
	// notification listener alive past the first event so the terminal Done
	// signal can be delivered.
	DoneChan chan struct{}
}

// DefaultNotifierOptions returns the set of default options for the notifier.
func DefaultNotifierOptions() *NotifierOptions {
	return &NotifierOptions{}
}

// NotifierOption is a functional option that allows a caller to modify the
// events received from the notifier.
type NotifierOption func(*NotifierOptions)

// WithIncludeBlock is an optional argument that allows the caller to specify
// that the block that mined a transaction should be included in the response.
func WithIncludeBlock() NotifierOption {
	return func(o *NotifierOptions) {
		o.IncludeBlock = true
	}
}

// WithReOrgChan configures a channel that will be sent on if the transaction is
// re-organized out of the chain. This channel being set will also imply that we
// don't cancel the notification listener after having received one confirmation
// event. That means the caller manually needs to cancel the passed in context
// to cancel being notified once the required number of confirmations have been
// reached.
func WithReOrgChan(reOrgChan chan struct{}) NotifierOption {
	return func(o *NotifierOptions) {
		o.ReOrgChan = reOrgChan
	}
}

// WithReOrgDepthChan configures a channel that will be sent the depth of a
// re-org whenever the transaction/spend is re-organized out of the chain. Like
// WithReOrgChan, setting this keeps the notification listener alive past the
// first event, so the caller must cancel the passed in context to stop being
// notified. Only confirmation notifications populate a non-zero depth.
func WithReOrgDepthChan(reOrgDepthChan chan int32) NotifierOption {
	return func(o *NotifierOptions) {
		o.ReOrgDepthChan = reOrgDepthChan
	}
}

// WithDoneChan configures a channel that will be sent on once the notification
// has reached the backend's re-org safety depth and is therefore complete.
// Setting this keeps the notification listener alive past the first event so
// the terminal Done signal can be delivered.
func WithDoneChan(doneChan chan struct{}) NotifierOption {
	return func(o *NotifierOptions) {
		o.DoneChan = doneChan
	}
}

// ChainNotifierClient exposes base lightning functionality.
type ChainNotifierClient interface {
	ServiceClient[chainrpc.ChainNotifierClient]

	RegisterBlockEpochNtfn(ctx context.Context) (
		chan int32, chan error, error)

	RegisterConfirmationsNtfn(ctx context.Context, txid *chainhash.Hash,
		pkScript []byte, numConfs, heightHint int32,
		opts ...NotifierOption) (chan *chainntnfs.TxConfirmation,
		chan error, error)

	RegisterSpendNtfn(ctx context.Context,
		outpoint *wire.OutPoint, pkScript []byte, heightHint int32,
		optFuncs ...NotifierOption) (chan *chainntnfs.SpendDetail,
		chan error, error)
}

type chainNotifierClient struct {
	client   chainrpc.ChainNotifierClient
	chainMac serializedMacaroon
	timeout  time.Duration

	wg sync.WaitGroup
}

// A compile time check to ensure that chainNotifierClient implements the
// ChainNotifierClient interface.
var _ ChainNotifierClient = (*chainNotifierClient)(nil)

func newChainNotifierClient(conn grpc.ClientConnInterface,
	chainMac serializedMacaroon, timeout time.Duration) *chainNotifierClient {

	return &chainNotifierClient{
		client:   chainrpc.NewChainNotifierClient(conn),
		chainMac: chainMac,
		timeout:  timeout,
	}
}

func (s *chainNotifierClient) WaitForFinished() {
	s.wg.Wait()
}

// RawClientWithMacAuth returns a context with the proper macaroon
// authentication, the default RPC timeout, and the raw client.
func (s *chainNotifierClient) RawClientWithMacAuth(
	parentCtx context.Context) (context.Context, time.Duration,
	chainrpc.ChainNotifierClient) {

	return s.chainMac.WithMacaroonAuth(parentCtx), s.timeout, s.client
}

func (s *chainNotifierClient) RegisterSpendNtfn(ctx context.Context,
	outpoint *wire.OutPoint, pkScript []byte, heightHint int32,
	optFuncs ...NotifierOption) (chan *chainntnfs.SpendDetail, chan error,
	error) {

	opts := DefaultNotifierOptions()
	for _, optFunc := range optFuncs {
		optFunc(opts)
	}
	if opts.IncludeBlock {
		return nil, nil, fmt.Errorf("option IncludeBlock is not " +
			"supported by RegisterSpendNtfn")
	}

	var rpcOutpoint *chainrpc.Outpoint
	if outpoint != nil {
		rpcOutpoint = &chainrpc.Outpoint{
			Hash:  outpoint.Hash[:],
			Index: outpoint.Index,
		}
	}

	macaroonAuth := s.chainMac.WithMacaroonAuth(ctx)
	resp, err := s.client.RegisterSpendNtfn(macaroonAuth, &chainrpc.SpendRequest{
		HeightHint: uint32(heightHint),
		Outpoint:   rpcOutpoint,
		Script:     pkScript,
	})
	if err != nil {
		return nil, nil, err
	}

	spendChan := make(chan *chainntnfs.SpendDetail, 1)
	errChan := make(chan error, 1)

	processSpendDetail := func(d *chainrpc.SpendDetails) error {
		outpointHash, err := chainhash.NewHash(d.SpendingOutpoint.Hash)
		if err != nil {
			return err
		}
		txHash, err := chainhash.NewHash(d.SpendingTxHash)
		if err != nil {
			return err
		}
		tx, err := decodeTx(d.RawSpendingTx)
		if err != nil {
			return err
		}
		spend := &chainntnfs.SpendDetail{
			SpentOutPoint: &wire.OutPoint{
				Hash:  *outpointHash,
				Index: d.SpendingOutpoint.Index,
			},
			SpenderTxHash:     txHash,
			SpenderInputIndex: d.SpendingInputIndex,
			SpendingTx:        tx,
			SpendingHeight:    int32(d.SpendingHeight),
		}

		select {
		case spendChan <- spend:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// reorgAware reports whether the caller asked to keep the listener
	// alive past the first event to observe re-orgs or the terminal Done
	// signal.
	reorgAware := opts.ReOrgChan != nil || opts.ReOrgDepthChan != nil ||
		opts.DoneChan != nil

	processReorg := func(depth int32) {
		deliver(ctx, opts.ReOrgChan, struct{}{})
		deliver(ctx, opts.ReOrgDepthChan, depth)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			spendEvent, err := resp.Recv()
			if err != nil {
				errChan <- err
				return
			}

			switch c := spendEvent.Event.(type) {
			case *chainrpc.SpendEvent_Spend:
				err := processSpendDetail(c.Spend)
				if err != nil {
					errChan <- err

					return
				}

				// If we're running in re-org aware mode, then
				// we don't return here, since we might want to
				// be informed about the new block we got
				// confirmed in after a re-org.
				if !reorgAware {
					return
				}

			case *chainrpc.SpendEvent_Reorg:
				// The spend notifier does not track re-org
				// depth, so a depth of 0 is forwarded.
				processReorg(0)

			// The spend has reached the backend's re-org safety
			// depth: deliver the terminal Done signal and stop.
			case *chainrpc.SpendEvent_Done:
				deliver(ctx, opts.DoneChan, struct{}{})

				return

			// Nil event, should never happen.
			case nil:
				errChan <- fmt.Errorf("spend event empty")
				return

			// Unexpected type.
			default:
				errChan <- fmt.Errorf("spend event has " +
					"unexpected type")
				return
			}
		}
	}()

	return spendChan, errChan, nil
}

// deliver performs a non-blocking-until-ctx send of v on ch, if ch is
// non-nil. It is used to fan re-org/depth/done signals out to the optional
// caller-supplied channels without blocking the notifier read loop beyond the
// lifetime of the registration context.
func deliver[T any](ctx context.Context, ch chan T, v T) {
	if ch == nil {
		return
	}

	select {
	case ch <- v:
	case <-ctx.Done():
	}
}

func (s *chainNotifierClient) RegisterConfirmationsNtfn(ctx context.Context,
	txid *chainhash.Hash, pkScript []byte, numConfs, heightHint int32,
	optFuncs ...NotifierOption) (chan *chainntnfs.TxConfirmation,
	chan error, error) {

	opts := DefaultNotifierOptions()
	for _, optFunc := range optFuncs {
		optFunc(opts)
	}

	var txidSlice []byte
	if txid != nil {
		txidSlice = txid[:]
	}
	confStream, err := s.client.RegisterConfirmationsNtfn(
		s.chainMac.WithMacaroonAuth(ctx), &chainrpc.ConfRequest{
			Script:       pkScript,
			NumConfs:     uint32(numConfs),
			HeightHint:   uint32(heightHint),
			Txid:         txidSlice,
			IncludeBlock: opts.IncludeBlock,
		},
	)
	if err != nil {
		return nil, nil, err
	}

	confChan := make(chan *chainntnfs.TxConfirmation, 1)
	errChan := make(chan error, 1)

	// reorgAware reports whether the caller asked to keep the listener
	// alive past the first confirmation to observe re-orgs or the terminal
	// Done signal.
	reorgAware := opts.ReOrgChan != nil || opts.ReOrgDepthChan != nil ||
		opts.DoneChan != nil

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			var confEvent *chainrpc.ConfEvent
			confEvent, err := confStream.Recv()
			if err != nil {
				errChan <- err
				return
			}

			switch c := confEvent.Event.(type) {
			// Script confirmed.
			case *chainrpc.ConfEvent_Conf:
				tx, err := decodeTx(c.Conf.RawTx)
				if err != nil {
					errChan <- err
					return
				}

				var block *wire.MsgBlock
				if opts.IncludeBlock {
					block, err = decodeBlock(
						c.Conf.RawBlock,
					)
					if err != nil {
						errChan <- err
						return
					}
				}

				blockHash, err := chainhash.NewHash(
					c.Conf.BlockHash,
				)
				if err != nil {
					errChan <- err
					return
				}

				conf := &chainntnfs.TxConfirmation{
					BlockHeight: c.Conf.BlockHeight,
					BlockHash:   blockHash,
					Tx:          tx,
					TxIndex:     c.Conf.TxIndex,
					Block:       block,
				}

				select {
				case confChan <- conf:
				case <-ctx.Done():
					return
				}

				// If we're running in re-org aware mode, then
				// we don't return here, since we might want to
				// be informed about the new block we got
				// confirmed in after a re-org.
				if !reorgAware {
					return
				}

			// On a re-org, we signal and forward the re-org depth,
			// but only if the caller requested to be notified.
			case *chainrpc.ConfEvent_Reorg:
				deliver(ctx, opts.ReOrgChan, struct{}{})
				deliver(
					ctx, opts.ReOrgDepthChan,
					int32(c.Reorg.Depth),
				)
				continue

			// The confirmation has reached the backend's re-org
			// safety depth: deliver the terminal Done signal and
			// stop.
			case *chainrpc.ConfEvent_Done:
				deliver(ctx, opts.DoneChan, struct{}{})

				return

			// Nil event, should never happen.
			case nil:
				errChan <- fmt.Errorf("conf event empty")
				return

			// Unexpected type.
			default:
				errChan <- fmt.Errorf("conf event has " +
					"unexpected type")
				return
			}
		}
	}()

	return confChan, errChan, nil
}

func (s *chainNotifierClient) RegisterBlockEpochNtfn(ctx context.Context) (
	chan int32, chan error, error) {

	blockEpochClient, err := s.client.RegisterBlockEpochNtfn(
		s.chainMac.WithMacaroonAuth(ctx), &chainrpc.BlockEpoch{},
	)
	if err != nil {
		return nil, nil, err
	}

	blockErrorChan := make(chan error, 1)
	blockEpochChan := make(chan int32)

	// Start block epoch goroutine.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			epoch, err := blockEpochClient.Recv()
			if err != nil {
				blockErrorChan <- err
				return
			}

			select {
			case blockEpochChan <- int32(epoch.Height):
			case <-ctx.Done():
				return
			}
		}
	}()

	return blockEpochChan, blockErrorChan, nil
}
