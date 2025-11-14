package lndclient

import (
	"context"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// stubChainNotifierClient implements chainrpc.ChainNotifierClient with retry
// aware behavior for testing.
type stubChainNotifierClient struct {
	chainrpc.ChainNotifierClient

	blockAttempts       int
	blockSucceedAfter   int
	confirmAttempts     int
	confirmSucceedAfter int
	spendAttempts       int
	spendSucceedAfter   int
}

func (s *stubChainNotifierClient) RegisterBlockEpochNtfn(ctx context.Context,
	_ *chainrpc.BlockEpoch, _ ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterBlockEpochNtfnClient, error) {

	s.blockAttempts++
	if s.blockAttempts <= s.blockSucceedAfter {
		return nil, status.Error(
			codes.Unknown, chainNotifierStartupMessage,
		)
	}

	return &stubBlockEpochStream{
		stubClientStream: newStubClientStream(ctx),
	}, nil
}

func (s *stubChainNotifierClient) RegisterConfirmationsNtfn(ctx context.Context,
	_ *chainrpc.ConfRequest, _ ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterConfirmationsNtfnClient, error) {

	s.confirmAttempts++
	if s.confirmAttempts <= s.confirmSucceedAfter {
		return nil, status.Error(
			codes.Unknown, chainNotifierStartupMessage,
		)
	}

	return &stubConfirmationsStream{
		stubClientStream: newStubClientStream(ctx),
	}, nil
}

func (s *stubChainNotifierClient) RegisterSpendNtfn(ctx context.Context,
	_ *chainrpc.SpendRequest, _ ...grpc.CallOption) (
	chainrpc.ChainNotifier_RegisterSpendNtfnClient, error) {

	s.spendAttempts++
	if s.spendAttempts <= s.spendSucceedAfter {
		return nil, status.Error(
			codes.Unknown, chainNotifierStartupMessage,
		)
	}

	return &stubSpendStream{
		stubClientStream: newStubClientStream(ctx),
	}, nil
}

// stubClientStream is a minimal grpc.ClientStream implementation that respects
// context cancellation.
type stubClientStream struct {
	ctx context.Context
}

func newStubClientStream(ctx context.Context) *stubClientStream {
	return &stubClientStream{ctx: ctx}
}

func (s *stubClientStream) Header() (metadata.MD, error) {
	return nil, nil
}

func (s *stubClientStream) Trailer() metadata.MD {
	return nil
}

func (s *stubClientStream) CloseSend() error {
	return nil
}

func (s *stubClientStream) Context() context.Context {
	return s.ctx
}

func (s *stubClientStream) SendMsg(interface{}) error {
	return nil
}

func (s *stubClientStream) RecvMsg(interface{}) error {
	<-s.ctx.Done()

	return s.ctx.Err()
}

type stubBlockEpochStream struct {
	*stubClientStream
}

func (s *stubBlockEpochStream) Recv() (*chainrpc.BlockEpoch, error) {
	<-s.Context().Done()

	return nil, s.Context().Err()
}

type stubConfirmationsStream struct {
	*stubClientStream
}

func (s *stubConfirmationsStream) Recv() (*chainrpc.ConfEvent, error) {
	<-s.Context().Done()

	return nil, s.Context().Err()
}

type stubSpendStream struct {
	*stubClientStream
}

func (s *stubSpendStream) Recv() (*chainrpc.SpendEvent, error) {
	<-s.Context().Done()

	return nil, s.Context().Err()
}

// TestRegisterBlockEpochNtfnRetries ensures block epoch subscriptions retry
// until the notifier RPC is ready.
func TestRegisterBlockEpochNtfnRetries(t *testing.T) {
	t.Parallel()

	stub := &stubChainNotifierClient{
		blockSucceedAfter: 1,
	}

	client := &chainNotifierClient{
		client:   stub,
		chainMac: serializedMacaroon("test"),
		timeout:  time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	blockChan, errChan, err := client.RegisterBlockEpochNtfn(ctx)
	require.NoError(t, err)
	require.NotNil(t, blockChan)
	require.NotNil(t, errChan)
	require.Equal(t, 2, stub.blockAttempts)

	cancel()
}

// TestRegisterConfirmationsNtfnRetries ensures confirmation subscriptions retry
// until lnd exposes the notifier RPC.
func TestRegisterConfirmationsNtfnRetries(t *testing.T) {
	t.Parallel()

	stub := &stubChainNotifierClient{
		confirmSucceedAfter: 2,
	}

	client := &chainNotifierClient{
		client:   stub,
		chainMac: serializedMacaroon("test"),
		timeout:  time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	confChan, errChan, err := client.RegisterConfirmationsNtfn(
		ctx, nil, nil, 1, 0,
	)
	require.NoError(t, err)
	require.NotNil(t, confChan)
	require.NotNil(t, errChan)
	require.Equal(t, 3, stub.confirmAttempts)

	cancel()
}

// TestRegisterSpendNtfnRetries ensures spend subscriptions retry until the
// notifier RPC is ready.
func TestRegisterSpendNtfnRetries(t *testing.T) {
	t.Parallel()

	stub := &stubChainNotifierClient{
		spendSucceedAfter: 1,
	}

	client := &chainNotifierClient{
		client:   stub,
		chainMac: serializedMacaroon("test"),
		timeout:  time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spendChan, errChan, err := client.RegisterSpendNtfn(ctx, nil, nil, 0)
	require.NoError(t, err)
	require.NotNil(t, spendChan)
	require.NotNil(t, errChan)
	require.Equal(t, 2, stub.spendAttempts)
}

// TestIsChainNotifierStartingErr ensures we correctly detect the startup lag
// error returned by lnd v0.20.0-rc3+.
func TestIsChainNotifierStartingErr(t *testing.T) {
	t.Parallel()

	require.True(t, isChainNotifierStartingErr(
		status.Error(codes.Unavailable, chainNotifierStartupMessage),
	))

	require.True(t, isChainNotifierStartingErr(
		status.Error(codes.Unknown, chainNotifierStartupMessage),
	))

	require.True(t, isChainNotifierStartingErr(
		status.Error(codes.Unavailable, "some other error"),
	))

	require.False(t, isChainNotifierStartingErr(nil))

	require.False(t, isChainNotifierStartingErr(
		status.Error(codes.Unknown, "some other error"),
	))
}
