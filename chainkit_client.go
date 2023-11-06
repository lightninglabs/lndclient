package lndclient

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"google.golang.org/grpc"
)

// ChainKitClient exposes chain functionality.
type ChainKitClient interface {
	// GetBlock returns a block given the corresponding block hash.
	GetBlock(ctx context.Context, hash chainhash.Hash) (*wire.MsgBlock,
		error)

	// GetBlockHeader returns a block header given the corresponding block
	// hash.
	GetBlockHeader(ctx context.Context,
		hash chainhash.Hash) (*wire.BlockHeader, error)

	// GetBestBlock returns the latest block hash and current height of the
	// valid most-work chain.
	GetBestBlock(ctx context.Context) (chainhash.Hash, int32, error)

	// GetBlockHash returns the hash of the block in the best blockchain
	// at the given height.
	GetBlockHash(ctx context.Context, blockHeight int64) (chainhash.Hash,
		error)
}

type chainKitClient struct {
	client   chainrpc.ChainKitClient
	chainMac serializedMacaroon
	timeout  time.Duration

	wg sync.WaitGroup
}

func newChainKitClient(conn grpc.ClientConnInterface,
	chainMac serializedMacaroon, timeout time.Duration) *chainKitClient {

	return &chainKitClient{
		client:   chainrpc.NewChainKitClient(conn),
		chainMac: chainMac,
		timeout:  timeout,
	}
}

func (s *chainKitClient) WaitForFinished() {
	s.wg.Wait()
}

// GetBlock returns a block given the corresponding block hash.
func (s *chainKitClient) GetBlock(ctxParent context.Context,
	hash chainhash.Hash) (*wire.MsgBlock, error) {

	ctx, cancel := context.WithTimeout(ctxParent, s.timeout)
	defer cancel()

	macaroonAuth := s.chainMac.WithMacaroonAuth(ctx)
	req := &chainrpc.GetBlockRequest{
		BlockHash: hash[:],
	}
	resp, err := s.client.GetBlock(macaroonAuth, req)
	if err != nil {
		return nil, err
	}

	// Convert raw block bytes into wire.MsgBlock.
	msgBlock := &wire.MsgBlock{}
	blockReader := bytes.NewReader(resp.RawBlock)
	err = msgBlock.Deserialize(blockReader)
	if err != nil {
		return nil, err
	}

	return msgBlock, nil
}

// GetBlockHeader returns a block header given the corresponding block hash.
func (s *chainKitClient) GetBlockHeader(ctxParent context.Context,
	hash chainhash.Hash) (*wire.BlockHeader, error) {

	ctx, cancel := context.WithTimeout(ctxParent, s.timeout)
	defer cancel()

	macaroonAuth := s.chainMac.WithMacaroonAuth(ctx)
	req := &chainrpc.GetBlockHeaderRequest{
		BlockHash: hash[:],
	}
	resp, err := s.client.GetBlockHeader(macaroonAuth, req)
	if err != nil {
		return nil, err
	}

	// Convert raw block header bytes into wire.BlockHeader.
	blockHeader := &wire.BlockHeader{}
	blockReader := bytes.NewReader(resp.RawBlockHeader)
	err = blockHeader.Deserialize(blockReader)
	if err != nil {
		return nil, err
	}

	return blockHeader, nil
}

// GetBestBlock returns the block hash and current height from the valid
// most-work chain.
func (s *chainKitClient) GetBestBlock(ctxParent context.Context) (chainhash.Hash,
	int32, error) {

	ctx, cancel := context.WithTimeout(ctxParent, s.timeout)
	defer cancel()

	macaroonAuth := s.chainMac.WithMacaroonAuth(ctx)
	resp, err := s.client.GetBestBlock(
		macaroonAuth, &chainrpc.GetBestBlockRequest{},
	)
	if err != nil {
		return chainhash.Hash{}, 0, err
	}

	// Cast gRPC block hash bytes as chain hash type.
	var blockHash chainhash.Hash
	copy(blockHash[:], resp.BlockHash)

	return blockHash, resp.BlockHeight, nil
}

// GetBlockHash returns the hash of the block in the best blockchain at the
// given height.
func (s *chainKitClient) GetBlockHash(ctxParent context.Context,
	blockHeight int64) (chainhash.Hash, error) {

	ctx, cancel := context.WithTimeout(ctxParent, s.timeout)
	defer cancel()

	macaroonAuth := s.chainMac.WithMacaroonAuth(ctx)
	req := &chainrpc.GetBlockHashRequest{BlockHeight: blockHeight}
	resp, err := s.client.GetBlockHash(macaroonAuth, req)
	if err != nil {
		return chainhash.Hash{}, err
	}

	// Cast gRPC block hash bytes as chain hash type.
	var blockHash chainhash.Hash
	copy(blockHash[:], resp.BlockHash)

	return blockHash, nil
}
