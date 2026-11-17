package lndclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type scriptedConfStream struct {
	grpc.ClientStream

	events    []*chainrpc.ConfEvent
	recvCalls int
}

func (s *scriptedConfStream) Recv() (*chainrpc.ConfEvent, error) {
	s.recvCalls++
	if len(s.events) == 0 {
		return nil, io.EOF
	}

	event := s.events[0]
	s.events = s.events[1:]

	return event, nil
}

type scriptedSpendStream struct {
	grpc.ClientStream

	events    []*chainrpc.SpendEvent
	recvCalls int
}

func (s *scriptedSpendStream) Recv() (*chainrpc.SpendEvent, error) {
	s.recvCalls++
	if len(s.events) == 0 {
		return nil, io.EOF
	}

	event := s.events[0]
	s.events = s.events[1:]

	return event, nil
}

type mockChainNotifierRPC struct {
	confStream  chainrpc.ChainNotifier_RegisterConfirmationsNtfnClient
	spendStream chainrpc.ChainNotifier_RegisterSpendNtfnClient
}

func (m *mockChainNotifierRPC) RegisterConfirmationsNtfn(context.Context,
	*chainrpc.ConfRequest, ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterConfirmationsNtfnClient, error) {

	if m.confStream == nil {
		return nil, fmt.Errorf("unexpected confirmation registration")
	}

	return m.confStream, nil
}

func (m *mockChainNotifierRPC) RegisterSpendNtfn(context.Context,
	*chainrpc.SpendRequest, ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterSpendNtfnClient, error) {

	if m.spendStream == nil {
		return nil, fmt.Errorf("unexpected spend registration")
	}

	return m.spendStream, nil
}

func (m *mockChainNotifierRPC) RegisterBlockEpochNtfn(context.Context,
	*chainrpc.BlockEpoch, ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterBlockEpochNtfnClient, error) {

	return nil, fmt.Errorf("unexpected block epoch registration")
}

// testTxBytes returns a serialized transaction suitable for notifier events.
func testTxBytes(t *testing.T) []byte {
	t.Helper()

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{})
	tx.AddTxOut(&wire.TxOut{
		Value:    1000,
		PkScript: []byte{0x51},
	})

	var txBuf bytes.Buffer
	require.NoError(t, tx.Serialize(&txBuf))

	return txBuf.Bytes()
}

// confEvent returns a valid confirmation event at the given height.
func confEvent(t *testing.T, height uint32) *chainrpc.ConfEvent {
	t.Helper()

	return &chainrpc.ConfEvent{
		Event: &chainrpc.ConfEvent_Conf{
			Conf: &chainrpc.ConfDetails{
				RawTx:       testTxBytes(t),
				BlockHash:   make([]byte, chainhash.HashSize),
				BlockHeight: height,
			},
		},
	}
}

// spendEvent returns a valid spend event at the given height.
func spendEvent(t *testing.T, height uint32) *chainrpc.SpendEvent {
	t.Helper()

	return &chainrpc.SpendEvent{
		Event: &chainrpc.SpendEvent_Spend{
			Spend: &chainrpc.SpendDetails{
				SpendingOutpoint: &chainrpc.Outpoint{
					Hash: make([]byte, chainhash.HashSize),
				},
				RawSpendingTx:  testTxBytes(t),
				SpendingTxHash: make([]byte, chainhash.HashSize),
				SpendingHeight: height,
			},
		},
	}
}

// TestConfirmationLifecycle verifies that confirmation, re-org depth,
// reconfirmation, and finality are all surfaced in order.
func TestConfirmationLifecycle(t *testing.T) {
	t.Parallel()

	stream := &scriptedConfStream{
		events: []*chainrpc.ConfEvent{
			confEvent(t, 100),
			{
				Event: &chainrpc.ConfEvent_Reorg{
					Reorg: &chainrpc.Reorg{Depth: 7},
				},
			},
			confEvent(t, 102),
			{
				Event: &chainrpc.ConfEvent_Done{
					Done: &chainrpc.Done{},
				},
			},
		},
	}
	client := &chainNotifierClient{
		client: &mockChainNotifierRPC{confStream: stream},
	}

	reorgChan := make(chan struct{}, 1)
	depthChan := make(chan int32, 1)
	doneChan := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	confs, errs, err := client.RegisterConfirmationsNtfn(
		ctx, nil, []byte{0x51}, 1, 1,
		WithReOrgChan(reorgChan), WithReOrgDepthChan(depthChan),
		WithDoneChan(doneChan),
	)
	require.NoError(t, err)

	select {
	case conf := <-confs:
		require.EqualValues(t, 100, conf.BlockHeight)
	case <-ctx.Done():
		t.Fatal("confirmation lifecycle timed out")
	}
	select {
	case <-reorgChan:
	case <-ctx.Done():
		t.Fatal("confirmation re-org signal timed out")
	}
	select {
	case depth := <-depthChan:
		require.EqualValues(t, 7, depth)
	case <-ctx.Done():
		t.Fatal("confirmation re-org depth timed out")
	}
	select {
	case conf := <-confs:
		require.EqualValues(t, 102, conf.BlockHeight)
	case <-ctx.Done():
		t.Fatal("reconfirmation timed out")
	}
	select {
	case <-doneChan:
	case <-ctx.Done():
		t.Fatal("confirmation Done signal timed out")
	}

	client.WaitForFinished()
	require.Equal(t, 4, stream.recvCalls)
	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

// TestSpendLifecycle verifies that spend, re-org, replacement spend, and
// finality are all surfaced in order. Spend notifications report depth zero.
func TestSpendLifecycle(t *testing.T) {
	t.Parallel()

	stream := &scriptedSpendStream{
		events: []*chainrpc.SpendEvent{
			spendEvent(t, 100),
			{
				Event: &chainrpc.SpendEvent_Reorg{
					Reorg: &chainrpc.Reorg{},
				},
			},
			spendEvent(t, 102),
			{
				Event: &chainrpc.SpendEvent_Done{
					Done: &chainrpc.Done{},
				},
			},
		},
	}
	client := &chainNotifierClient{
		client: &mockChainNotifierRPC{spendStream: stream},
	}

	reorgChan := make(chan struct{}, 1)
	depthChan := make(chan int32, 1)
	doneChan := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	spends, errs, err := client.RegisterSpendNtfn(
		ctx, nil, []byte{0x51}, 1, WithReOrgChan(reorgChan),
		WithReOrgDepthChan(depthChan), WithDoneChan(doneChan),
	)
	require.NoError(t, err)

	select {
	case spend := <-spends:
		require.EqualValues(t, 100, spend.SpendingHeight)
	case <-ctx.Done():
		t.Fatal("spend lifecycle timed out")
	}
	select {
	case <-reorgChan:
	case <-ctx.Done():
		t.Fatal("spend re-org signal timed out")
	}
	select {
	case depth := <-depthChan:
		require.Zero(t, depth)
	case <-ctx.Done():
		t.Fatal("spend re-org depth timed out")
	}
	select {
	case spend := <-spends:
		require.EqualValues(t, 102, spend.SpendingHeight)
	case <-ctx.Done():
		t.Fatal("replacement spend timed out")
	}
	select {
	case <-doneChan:
	case <-ctx.Done():
		t.Fatal("spend Done signal timed out")
	}

	client.WaitForFinished()
	require.Equal(t, 4, stream.recvCalls)
	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

// TestNotifierLegacySingleEvent verifies that callers which do not request
// lifecycle events retain the existing first-positive-event behavior.
func TestNotifierLegacySingleEvent(t *testing.T) {
	t.Parallel()

	stream := &scriptedConfStream{
		events: []*chainrpc.ConfEvent{
			confEvent(t, 100), confEvent(t, 101),
		},
	}
	client := &chainNotifierClient{
		client: &mockChainNotifierRPC{confStream: stream},
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	confs, errs, err := client.RegisterConfirmationsNtfn(
		ctx, nil, []byte{0x51}, 1, 1,
	)
	require.NoError(t, err)

	select {
	case conf := <-confs:
		require.EqualValues(t, 100, conf.BlockHeight)
	case <-ctx.Done():
		t.Fatal("legacy confirmation timed out")
	}

	client.WaitForFinished()
	require.Equal(t, 1, stream.recvCalls)
	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}
