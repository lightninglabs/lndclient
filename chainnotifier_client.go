package lndclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"google.golang.org/grpc"
)

// notifierOptions is a set of functional options that allow callers to further
// modify the type of chain even notifications they receive.
type notifierOptions struct {
	// includeBlock if true, then the dispatched confirmation notification
	// will include the block that mined the transaction.
	includeBlock bool

	// reOrgChan if set, will be sent on if the transaction is re-organized
	// out of the chain. This channel being set will also imply that we
	// don't cancel the notification listener after having received one
	// confirmation event. That means the caller manually needs to cancel
	// the passed in context to cancel being notified once the required
	// number of confirmations have been reached.
	reOrgChan chan struct{}
}

// defaultNotifierOptions returns the set of default options for the notifier.
func defaultNotifierOptions() *notifierOptions {
	return &notifierOptions{}
}

// NotifierOption is a functional option that allows a caller to modify the
// events received from the notifier.
type NotifierOption func(*notifierOptions)

// WithIncludeBlock is an optional argument that allows the caller to specify
// that the block that mined a transaction should be included in the response.
func WithIncludeBlock() NotifierOption {
	return func(o *notifierOptions) {
		o.includeBlock = true
	}
}

// WithReOrgChan configures a channel that will be sent on if the transaction is
// re-organized out of the chain. This channel being set will also imply that we
// don't cancel the notification listener after having received one confirmation
// event. That means the caller manually needs to cancel the passed in context
// to cancel being notified once the required number of confirmations have been
// reached.
func WithReOrgChan(reOrgChan chan struct{}) NotifierOption {
	return func(o *notifierOptions) {
		o.reOrgChan = reOrgChan
	}
}

// ChainNotifierClient exposes base lightning functionality.
type ChainNotifierClient interface {
	RegisterBlockEpochNtfn(ctx context.Context) (
		chan int32, chan error, error)

	RegisterConfirmationsNtfn(ctx context.Context, txid *chainhash.Hash,
		pkScript []byte, numConfs, heightHint int32,
		opts ...NotifierOption) (chan *chainntnfs.TxConfirmation,
		chan error, error)

	RegisterSpendNtfn(ctx context.Context,
		outpoint *wire.OutPoint, pkScript []byte, heightHint int32) (
		chan *chainntnfs.SpendDetail, chan error, error)
}

type chainNotifierClient struct {
	client   chainrpc.ChainNotifierClient
	chainMac serializedMacaroon
	timeout  time.Duration

	wg sync.WaitGroup
}

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

func (s *chainNotifierClient) RegisterSpendNtfn(ctx context.Context,
	outpoint *wire.OutPoint, pkScript []byte, heightHint int32) (
	chan *chainntnfs.SpendDetail, chan error, error) {

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
		spendChan <- &chainntnfs.SpendDetail{
			SpentOutPoint: &wire.OutPoint{
				Hash:  *outpointHash,
				Index: d.SpendingOutpoint.Index,
			},
			SpenderTxHash:     txHash,
			SpenderInputIndex: d.SpendingInputIndex,
			SpendingTx:        tx,
			SpendingHeight:    int32(d.SpendingHeight),
		}

		return nil
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

			c, ok := spendEvent.Event.(*chainrpc.SpendEvent_Spend)
			if ok {
				err := processSpendDetail(c.Spend)
				if err != nil {
					errChan <- err
				}
				return
			}
		}
	}()

	return spendChan, errChan, nil
}

func (s *chainNotifierClient) RegisterConfirmationsNtfn(ctx context.Context,
	txid *chainhash.Hash, pkScript []byte, numConfs, heightHint int32,
	optFuncs ...NotifierOption) (chan *chainntnfs.TxConfirmation,
	chan error, error) {

	opts := defaultNotifierOptions()
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
			IncludeBlock: opts.includeBlock,
		},
	)
	if err != nil {
		return nil, nil, err
	}

	confChan := make(chan *chainntnfs.TxConfirmation, 1)
	errChan := make(chan error, 1)

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
				if opts.includeBlock {
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

				confChan <- &chainntnfs.TxConfirmation{
					BlockHeight: c.Conf.BlockHeight,
					BlockHash:   blockHash,
					Tx:          tx,
					TxIndex:     c.Conf.TxIndex,
					Block:       block,
				}

				// If we're running in re-org aware mode, then
				// we don't return here, since we might want to
				// be informed about the new block we got
				// confirmed in after a re-org.
				if opts.reOrgChan == nil {
					return
				}

			// On a re-org, we just need to signal, we don't have
			// any additional information. But we only signal if the
			// caller requested to be notified about re-orgs.
			case *chainrpc.ConfEvent_Reorg:
				if opts.reOrgChan != nil {
					select {
					case opts.reOrgChan <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
				continue

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
