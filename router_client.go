package lndclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/channeldb"
	invpkg "github.com/lightningnetwork/lnd/invoices"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/record"
	"github.com/lightningnetwork/lnd/routing/route"
	"github.com/lightningnetwork/lnd/zpay32"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrRouterShuttingDown is returned when a long-lived call is killed because
// the router is shutting down.
var ErrRouterShuttingDown = errors.New("router shutting down")

// RouterClient exposes payment functionality.
type RouterClient interface {
	// SendPayment attempts to route a payment to the final destination. The
	// call returns a payment update stream and an error stream.
	SendPayment(ctx context.Context, request SendPaymentRequest) (
		chan PaymentStatus, chan error, error)

	// TrackPayment picks up a previously started payment and returns a
	// payment update stream and an error stream.
	TrackPayment(ctx context.Context, hash lntypes.Hash) (
		chan PaymentStatus, chan error, error)

	// EstimateRouteFee uses the channel router's internal state to estimate
	// the routing cost of the given amount to the destination node.
	EstimateRouteFee(ctx context.Context, dest route.Vertex,
		amt btcutil.Amount) (lnwire.MilliSatoshi, error)

	// SubscribeHtlcEvents subscribes to a stream of htlc events from the
	// router.
	SubscribeHtlcEvents(ctx context.Context) (<-chan *routerrpc.HtlcEvent,
		<-chan error, error)

	// InterceptHtlcs intercepts htlcs, using the handling function provided
	// to respond to htlcs. This function blocks, and can be terminated by
	// canceling the context provided. The handler provided should exit on
	// context cancel, and must be thread-safe. On exit, all htlcs that are
	// currently held will be released by lnd.
	InterceptHtlcs(ctx context.Context,
		handler HtlcInterceptHandler) error

	// QueryMissionControl will query Mission Control state from lnd.
	QueryMissionControl(ctx context.Context) ([]MissionControlEntry, error)

	// ImportMissionControl imports a set of pathfinding results to lnd.
	ImportMissionControl(ctx context.Context,
		entries []MissionControlEntry, force bool) error

	// ResetMissionControl resets the Mission Control state of lnd.
	ResetMissionControl(ctx context.Context) error

	// UpdateChanStatus attempts to manually set the state of a channel
	// (enabled, disabled, or auto).
	UpdateChanStatus(ctx context.Context,
		channel *wire.OutPoint, action routerrpc.ChanStatusAction) error
}

// PaymentStatus describe the state of a payment.
type PaymentStatus struct {
	State lnrpc.Payment_PaymentStatus

	// FailureReason is the reason why the payment failed. Only set when
	// State is Failed.
	FailureReason lnrpc.PaymentFailureReason

	Preimage      lntypes.Preimage
	Fee           lnwire.MilliSatoshi
	Value         lnwire.MilliSatoshi
	InFlightAmt   lnwire.MilliSatoshi
	InFlightHtlcs int

	Htlcs []*HtlcAttempt
}

func (p PaymentStatus) String() string {
	text := fmt.Sprintf("state=%v", p.State)
	if p.State == lnrpc.Payment_IN_FLIGHT {
		text += fmt.Sprintf(", inflight_htlcs=%v, inflight_amt=%v",
			p.InFlightHtlcs, p.InFlightAmt)
	}

	return text
}

// HtlcAttempt provides information about a htlc sent as part of a payment.
type HtlcAttempt struct {
	// The status of the HTLC.
	Status lnrpc.HTLCAttempt_HTLCStatus

	// The route taken by this HTLC.
	Route *lnrpc.Route

	// AttemptTime is the time that the htlc was dispatched.
	AttemptTime time.Time

	// ResolveTime is the time the htlc was settled or failed.
	ResolveTime time.Time

	// Failure will be non-nil if the htlc failed, and provides more
	// information about the payment failure.
	Failure *HtlcFailure

	// Preimage is the preimage that was used to settle the payment.
	Preimage lntypes.Preimage
}

// AmountInFlight returns the amount in flight for this htlc attempt that
// contributes to paying the final recipient, if any.
func (h *HtlcAttempt) AmountInFlight() lnwire.MilliSatoshi {
	if h.Status != lnrpc.HTLCAttempt_IN_FLIGHT {
		return 0
	}

	if h.Route == nil || h.Route.Hops == nil {
		return 0
	}

	lastHop := h.Route.Hops[len(h.Route.Hops)-1]
	return lnwire.MilliSatoshi(lastHop.AmtToForwardMsat)
}

// String returns a string representation of a htlc attempt.
func (h *HtlcAttempt) String() string {
	return fmt.Sprintf("Htlc attempt status: %v, attempted at: %v, "+
		"resolved at: %v, preimage: %v", h.Status, h.AttemptTime,
		h.ResolveTime, h.Preimage)
}

// NewHtlcAttempt creates a htlc attempt from its rpc counterpart.
func NewHtlcAttempt(rpcAttempt *lnrpc.HTLCAttempt) (*HtlcAttempt, error) {
	attempt := &HtlcAttempt{
		Status: rpcAttempt.Status,
		Route:  rpcAttempt.Route,
	}

	if rpcAttempt.AttemptTimeNs != 0 {
		attempt.AttemptTime = time.Unix(0, rpcAttempt.AttemptTimeNs)
	}

	if rpcAttempt.ResolveTimeNs != 0 {
		attempt.ResolveTime = time.Unix(0, rpcAttempt.ResolveTimeNs)
	}

	if rpcAttempt.Failure != nil {
		attempt.Failure = NewHtlcFailure(rpcAttempt.Failure)
	}

	if rpcAttempt.Preimage != nil {
		var err error
		attempt.Preimage, err = lntypes.MakePreimage(
			rpcAttempt.Preimage,
		)
		if err != nil {
			return nil, err
		}
	}

	return attempt, nil
}

// HtlcFailure provides information about a htlc attempt failure.
type HtlcFailure struct {
	// Code is the failure code as defined in the lightning spec.
	Code lnrpc.Failure_FailureCode

	// FailureSourceIndex is the position in the path of the intermediate
	// or final node that generated the failure message. A value of 0
	// indicates that the failure occurred at the sender.
	FailureSourceIndex uint32
}

// String returns a string representation of a htlc failure.
func (h *HtlcFailure) String() string {
	return fmt.Sprintf("Htlc failure code: %v, index: %v", h.Code,
		h.FailureSourceIndex)
}

// NewHtlcFailure creates a htlc failure from its rpc counterpart.
func NewHtlcFailure(rpcFailure *lnrpc.Failure) *HtlcFailure {
	if rpcFailure == nil {
		return nil
	}

	return &HtlcFailure{
		Code:               rpcFailure.Code,
		FailureSourceIndex: rpcFailure.FailureSourceIndex,
	}
}

// SendPaymentRequest defines the payment parameters for a new payment.
type SendPaymentRequest struct {
	// Invoice is an encoded payment request. The individual payment
	// parameters Target, Amount, PaymentHash, FinalCLTVDelta and RouteHints
	// are only processed when the Invoice field is empty.
	Invoice string

	// MaxFee is the fee limit for this payment.
	MaxFee btcutil.Amount

	// MaxFeeMsat is the fee limit for this payment in millisatoshis.
	// MaxFee and MaxFeeMsat are mutually exclusive.
	MaxFeeMsat lnwire.MilliSatoshi

	// MaxCltv is the maximum timelock for this payment. If nil, there is no
	// maximum.
	MaxCltv *int32

	// OutgoingChanIds is a restriction on the set of possible outgoing
	// channels. If nil or empty, there is no restriction.
	OutgoingChanIds []uint64

	// Timeout is the payment loop timeout. After this time, no new payment
	// attempts will be started.
	Timeout time.Duration

	// Target is the node in which the payment should be routed towards.
	Target route.Vertex

	// Amount is the value of the payment to send through the network in
	// satoshis.
	Amount btcutil.Amount

	// PaymentHash is the r-hash value to use within the HTLC extended to
	// the first hop.
	PaymentHash *lntypes.Hash

	// FinalCLTVDelta is the CTLV expiry delta to use for the _final_ hop
	// in the route. This means that the final hop will have a CLTV delta
	// of at least: currentHeight + FinalCLTVDelta.
	FinalCLTVDelta uint16

	// RouteHints represents the different routing hints that can be used to
	// assist a payment in reaching its destination successfully. These
	// hints will act as intermediate hops along the route.
	//
	// NOTE: This is optional unless required by the payment. When providing
	// multiple routes, ensure the hop hints within each route are chained
	// together and sorted in forward order in order to reach the
	// destination successfully.
	RouteHints [][]zpay32.HopHint

	// LastHopPubkey is the pubkey of the last hop of the route taken
	// for this payment. If empty, any hop may be used.
	LastHopPubkey *route.Vertex

	// MaxParts is the maximum number of partial payments that may be used
	// to complete the full amount.
	MaxParts uint32

	// KeySend is set to true if the tlv payload will include the preimage.
	KeySend bool

	// CustomRecords holds the custom TLV records that will be added to the
	// payment.
	CustomRecords map[uint64][]byte

	// If set, circular payments to self are permitted.
	AllowSelfPayment bool

	// The time preference for this payment. Set to -1 to optimize for fees
	// only, to 1 to optimize for reliability only or a value in-between for
	// a mix.
	TimePref float64
}

// InterceptedHtlc contains information about a htlc that was intercepted in
// lnd's switch.
type InterceptedHtlc struct {
	// IncomingCircuitKey is lnd's unique identfier for the incoming htlc.
	IncomingCircuitKey invpkg.CircuitKey

	// Hash is the payment hash for the htlc. This may not be unique for
	// MPP htlcs.
	Hash lntypes.Hash

	// AmountInMsat is the incoming htlc amount.
	AmountInMsat lnwire.MilliSatoshi

	// AmountOutMsat is the outgoing htlc amount.
	AmountOutMsat lnwire.MilliSatoshi

	// IncomingExpiryHeight is the expiry height of the incoming htlc.
	IncomingExpiryHeight uint32

	// OutgoingExpiryHeight is the expiry height of the outgoing htlcs.
	OutgoingExpiryHeight uint32

	// OutgoingChannelID is the outgoing channel id proposed by the sender.
	// Since lnd has non-strict forwarding, this may not be the channel that
	// the htlc ends up being forwarded on.
	OutgoingChannelID lnwire.ShortChannelID

	// CustomRecords holds the custom TLV records that were added to the
	// payment.
	CustomRecords map[uint64][]byte

	// OnionBlob is the onion blob for the next hop.
	OnionBlob []byte
}

// HtlcInterceptHandler is a function signature for handling code for htlc
// interception.
type HtlcInterceptHandler func(context.Context,
	InterceptedHtlc) (*InterceptedHtlcResponse, error)

// InterceptorAction represents the different actions we can take for an
// intercepted htlc.
type InterceptorAction uint8

const (
	// InterceptorActionSettle indicates that an intercepted htlc should
	// be settled.
	InterceptorActionSettle InterceptorAction = iota

	// InterceptorActionFail indicates that an intercepted htlc should be
	// failed.
	InterceptorActionFail

	// InterceptorActionResume indicates that an intercepted hltc should be
	// resumed as normal.
	InterceptorActionResume
)

// InterceptedHtlcResponse contains the actions that must be taken for an
// intercepted htlc.
type InterceptedHtlcResponse struct {
	// Preimage is the preimage to settle a htlc with, this value must be
	// set if the interceptor action is to settle.
	Preimage *lntypes.Preimage

	// Action is the action that should be taken for the htlc that is
	// intercepted.
	Action InterceptorAction
}

// routerClient is a wrapper around the generated routerrpc proxy.
type routerClient struct {
	client       routerrpc.RouterClient
	routerKitMac serializedMacaroon
	timeout      time.Duration
	quitOnce     sync.Once
	quit         chan struct{}
	wg           sync.WaitGroup
}

func newRouterClient(conn grpc.ClientConnInterface,
	routerKitMac serializedMacaroon, timeout time.Duration) *routerClient {

	return &routerClient{
		client:       routerrpc.NewRouterClient(conn),
		routerKitMac: routerKitMac,
		timeout:      timeout,
		quit:         make(chan struct{}),
	}
}

// WaitForFinished sends the signal for the router client to shut down and waits
// for all goroutines to exit.
func (r *routerClient) WaitForFinished() {
	r.quitOnce.Do(func() {
		close(r.quit)
	})

	r.wg.Wait()
}

// SendPayment attempts to route a payment to the final destination. The call
// returns a payment update stream and an error stream.
func (r *routerClient) SendPayment(ctx context.Context,
	request SendPaymentRequest) (chan PaymentStatus, chan error, error) {

	rpcCtx := r.routerKitMac.WithMacaroonAuth(ctx)
	rpcReq := &routerrpc.SendPaymentRequest{
		FeeLimitSat:      int64(request.MaxFee),
		FeeLimitMsat:     int64(request.MaxFeeMsat),
		PaymentRequest:   request.Invoice,
		TimeoutSeconds:   int32(request.Timeout.Seconds()),
		MaxParts:         request.MaxParts,
		OutgoingChanIds:  request.OutgoingChanIds,
		AllowSelfPayment: request.AllowSelfPayment,
		TimePref:         request.TimePref,
	}
	if request.MaxCltv != nil {
		rpcReq.CltvLimit = *request.MaxCltv
	}

	if request.LastHopPubkey != nil {
		rpcReq.LastHopPubkey = request.LastHopPubkey[:]
	}

	rpcReq.DestCustomRecords = request.CustomRecords

	if request.KeySend {
		if request.PaymentHash != nil {
			return nil, nil, fmt.Errorf("keysend payment must not " +
				"include a preset payment hash")
		}

		var preimage lntypes.Preimage
		if _, err := rand.Read(preimage[:]); err != nil {
			return nil, nil, err
		}

		if rpcReq.DestCustomRecords == nil {
			rpcReq.DestCustomRecords = make(map[uint64][]byte)
		}

		// Override the payment hash.
		rpcReq.DestCustomRecords[record.KeySendType] = preimage[:]
		hash := preimage.Hash()
		request.PaymentHash = &hash
	}

	// Only if there is no payment request set, we will parse the individual
	// payment parameters.
	if request.Invoice == "" {
		rpcReq.Dest = request.Target[:]
		rpcReq.Amt = int64(request.Amount)
		rpcReq.PaymentHash = request.PaymentHash[:]
		rpcReq.FinalCltvDelta = int32(request.FinalCLTVDelta)

		routeHints, err := marshallRouteHints(request.RouteHints)
		if err != nil {
			return nil, nil, err
		}
		rpcReq.RouteHints = routeHints
	}

	stream, err := r.client.SendPaymentV2(rpcCtx, rpcReq)
	if err != nil {
		return nil, nil, err
	}

	return r.trackPayment(ctx, stream)
}

// TrackPayment picks up a previously started payment and returns a payment
// update stream and an error stream.
func (r *routerClient) TrackPayment(ctx context.Context,
	hash lntypes.Hash) (chan PaymentStatus, chan error, error) {

	ctx = r.routerKitMac.WithMacaroonAuth(ctx)
	stream, err := r.client.TrackPaymentV2(
		ctx, &routerrpc.TrackPaymentRequest{
			PaymentHash: hash[:],
		},
	)
	if err != nil {
		return nil, nil, err
	}

	return r.trackPayment(ctx, stream)
}

// trackPayment takes an update stream from either a SendPayment or a
// TrackPayment rpc call and converts it into distinct update and error streams.
// Once the payment reaches a final state, the status and error channels will
// be closed to signal that we are finished sending into them.
func (r *routerClient) trackPayment(ctx context.Context,
	stream routerrpc.Router_TrackPaymentV2Client) (chan PaymentStatus,
	chan error, error) {

	statusChan := make(chan PaymentStatus)
	errorChan := make(chan error, 1)
	go func() {
		for {
			payment, err := stream.Recv()
			if err != nil {
				// If we get an EOF error, the payment has
				// reached a final state and the server is
				// finished sending us updates. We close both
				// channels to signal that we are done sending
				// values on them and return.
				if err == io.EOF {
					close(statusChan)
					close(errorChan)
					return
				}

				switch status.Convert(err).Code() {
				// NotFound is only expected as a response to
				// TrackPayment.
				case codes.NotFound:
					err = channeldb.ErrPaymentNotInitiated

				// NotFound is only expected as a response to
				// SendPayment.
				case codes.AlreadyExists:
					err = channeldb.ErrAlreadyPaid
				}

				errorChan <- err
				return
			}

			status, err := unmarshallPaymentStatus(payment)
			if err != nil {
				errorChan <- err
				return
			}

			select {
			case statusChan <- *status:
			case <-ctx.Done():
				return
			}
		}
	}()

	return statusChan, errorChan, nil
}

// EstimateRouteFee uses the channel router's internal state to estimate the
// routing cost of the given amount to the destination node.
func (r *routerClient) EstimateRouteFee(ctx context.Context, dest route.Vertex,
	amt btcutil.Amount) (lnwire.MilliSatoshi, error) {

	rpcCtx := r.routerKitMac.WithMacaroonAuth(ctx)
	rpcReq := &routerrpc.RouteFeeRequest{
		Dest:   dest[:],
		AmtSat: int64(amt),
	}

	rpcRes, err := r.client.EstimateRouteFee(rpcCtx, rpcReq)
	if err != nil {
		return 0, err
	}

	return lnwire.MilliSatoshi(rpcRes.RoutingFeeMsat), nil
}

// unmarshallPaymentStatus converts an rpc status update to the PaymentStatus
// type that is used throughout the application.
func unmarshallPaymentStatus(rpcPayment *lnrpc.Payment) (
	*PaymentStatus, error) {

	status := PaymentStatus{
		State: rpcPayment.Status,
		Htlcs: make([]*HtlcAttempt, len(rpcPayment.Htlcs)),
	}

	switch status.State {
	case lnrpc.Payment_SUCCEEDED:
		if rpcPayment.PaymentPreimage != "" {
			preimage, err := lntypes.MakePreimageFromStr(
				rpcPayment.PaymentPreimage,
			)
			if err != nil {
				return nil, err
			}
			status.Preimage = preimage
		}
		status.Fee = lnwire.MilliSatoshi(rpcPayment.FeeMsat)
		status.Value = lnwire.MilliSatoshi(rpcPayment.ValueMsat)

	case lnrpc.Payment_FAILED:
		status.FailureReason = rpcPayment.FailureReason
	}

	for i, htlc := range rpcPayment.Htlcs {
		attempt, err := NewHtlcAttempt(htlc)
		if err != nil {
			return nil, err
		}
		status.Htlcs[i] = attempt

		if htlc.Status != lnrpc.HTLCAttempt_IN_FLIGHT {
			continue
		}

		status.InFlightHtlcs++
		status.InFlightAmt += attempt.AmountInFlight()
	}

	return &status, nil
}

// marshallRouteHints marshalls a list of route hints.
func marshallRouteHints(routeHints [][]zpay32.HopHint) (
	[]*lnrpc.RouteHint, error) {

	rpcRouteHints := make([]*lnrpc.RouteHint, 0, len(routeHints))
	for _, routeHint := range routeHints {
		rpcRouteHint := make(
			[]*lnrpc.HopHint, 0, len(routeHint),
		)
		for _, hint := range routeHint {
			rpcHint, err := marshallHopHint(hint)
			if err != nil {
				return nil, err
			}

			rpcRouteHint = append(rpcRouteHint, rpcHint)
		}
		rpcRouteHints = append(rpcRouteHints, &lnrpc.RouteHint{
			HopHints: rpcRouteHint,
		})
	}

	return rpcRouteHints, nil
}

// marshallHopHint marshalls a single hop hint.
func marshallHopHint(hint zpay32.HopHint) (*lnrpc.HopHint, error) {
	nodeID, err := route.NewVertexFromBytes(
		hint.NodeID.SerializeCompressed(),
	)
	if err != nil {
		return nil, err
	}

	return &lnrpc.HopHint{
		ChanId:                    hint.ChannelID,
		CltvExpiryDelta:           uint32(hint.CLTVExpiryDelta),
		FeeBaseMsat:               hint.FeeBaseMSat,
		FeeProportionalMillionths: hint.FeeProportionalMillionths,
		NodeId:                    nodeID.String(),
	}, nil
}

// SubscribeHtlcEvents subscribes to a stream of htlc events from the router.
func (r *routerClient) SubscribeHtlcEvents(ctx context.Context) (
	<-chan *routerrpc.HtlcEvent, <-chan error, error) {

	stream, err := r.client.SubscribeHtlcEvents(
		r.routerKitMac.WithMacaroonAuth(ctx),
		&routerrpc.SubscribeHtlcEventsRequest{},
	)
	if err != nil {
		return nil, nil, err
	}

	// Buffer our error channel by 1 so we don't need to worry about the
	// client not listening or shutting down when we send an error.
	errChan := make(chan error, 1)
	htlcChan := make(chan *routerrpc.HtlcEvent)

	go func() {
		// Close our error and htlc channel when this loop exits to
		// signal that we will no longer be sending results.
		defer close(errChan)
		defer close(htlcChan)

		for {
			htlc, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}

			// Send the update to into our events channel, or exit
			// if our context has been cancelled.
			select {
			case htlcChan <- htlc:

			case <-ctx.Done():
				errChan <- ctx.Err()
				return
			}
		}
	}()

	return htlcChan, errChan, nil
}

// InterceptHtlcs intercepts htlcs on a node, using the handler function
// provided to provide the interceptor with interception decisions. The handler
// provided may block, but must exit if the context passed in is canceled, and
// must be thread-safe.
//
// There are a few ways in which this method can exit:
// - ctx canceled: the calling client cancels.
// - r.quit: the router is shut down.
// - lnd stream error: lnd has exited.
// - handler error: a critical error occurred while handing a htlc.
func (r *routerClient) InterceptHtlcs(ctx context.Context,
	handler HtlcInterceptHandler) error {

	// Create a child context that will be canceled when this function
	// exits. We use this context to be able to cancel goroutines when we
	// exit on errors, because the parent context won't be canceled in that
	// case.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := r.client.HtlcInterceptor(
		r.routerKitMac.WithMacaroonAuth(ctx),
	)
	if err != nil {
		return err
	}

	// Create an error channel that we'll send errors on if any of our
	// goroutines fail. We buffer by 1 so that the goroutine doesn't depend
	// on the stream being read, and select on context cancelation and
	// quit channel so that we do not block in the case where we exit with
	// multiple errors.
	errChan := make(chan error, 1)

	sendErr := func(err error) {
		select {
		case errChan <- err:
		case <-ctx.Done():
		case <-r.quit:
		}
	}

	// Start a goroutine that consumes interception requests from lnd and
	// sends them into our requests channel for handling. The requests
	// channel is not buffered because we expect all requests to be handled
	// until this function exits, at which point we expect our context to
	// be canceled or quit channel to be closed.
	requestChan := make(chan InterceptedHtlc)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		for {
			// Do a quick check whether our client context has been
			// canceled so that we can exit sooner if needed.
			if ctx.Err() != nil {
				return
			}

			request, err := stream.Recv()
			if err != nil {
				sendErr(err)
				return
			}

			hash, err := lntypes.MakeHash(request.PaymentHash)
			if err != nil {
				sendErr(err)
				return
			}

			if request.IncomingCircuitKey == nil {
				sendErr(errors.New("incoming circuit key " +
					"required"))

				return
			}

			chanIn := lnwire.NewShortChanIDFromInt(
				request.IncomingCircuitKey.ChanId,
			)
			chanOut := lnwire.NewShortChanIDFromInt(
				request.OutgoingRequestedChanId,
			)

			req := InterceptedHtlc{
				IncomingCircuitKey: invpkg.CircuitKey{
					ChanID: chanIn,
					HtlcID: request.IncomingCircuitKey.HtlcId,
				},
				Hash: hash,
				AmountInMsat: lnwire.MilliSatoshi(
					request.IncomingAmountMsat,
				),
				AmountOutMsat: lnwire.MilliSatoshi(
					request.OutgoingAmountMsat,
				),
				IncomingExpiryHeight: request.IncomingExpiry,
				OutgoingExpiryHeight: request.OutgoingExpiry,
				OutgoingChannelID:    chanOut,
				CustomRecords:        request.CustomRecords,
				OnionBlob:            request.OnionBlob,
			}

			// Try to send our interception request, failing on
			// context cancel or router exit. Under the hood, lnd
			// releases all htlcs that are held once we cancel the
			// htlc interceptor's run ctx, so it's ok if we never
			// end up delivering this request to a handler, since it
			// will be resumed by the underlying interceptor.
			select {
			case requestChan <- req:

			case <-r.quit:
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
			// shutdown the interceptor.
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()

				// Get a response from handler, this may block
				// for a while.
				resp, err := handler(ctx, request)
				if err != nil {
					sendErr(err)
					return
				}

				rpcResp, err := rpcInterceptorResponse(
					request, resp,
				)
				if err != nil {
					sendErr(err)
					return
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

		case <-r.quit:
			return ErrRouterShuttingDown

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func rpcInterceptorResponse(request InterceptedHtlc,
	response *InterceptedHtlcResponse) (
	*routerrpc.ForwardHtlcInterceptResponse, error) {

	rpcResp := &routerrpc.ForwardHtlcInterceptResponse{
		IncomingCircuitKey: &routerrpc.CircuitKey{
			ChanId: request.IncomingCircuitKey.ChanID.ToUint64(),
			HtlcId: request.IncomingCircuitKey.HtlcID,
		},
	}

	havePreimage := response.Preimage != nil
	if havePreimage {
		rpcResp.Preimage = response.Preimage[:]
	}

	switch response.Action {
	case InterceptorActionSettle:
		if !havePreimage {
			return nil, errors.New("preimage required for settle")
		}

		rpcResp.Action = routerrpc.ResolveHoldForwardAction_SETTLE

	case InterceptorActionFail:
		rpcResp.Action = routerrpc.ResolveHoldForwardAction_FAIL

	case InterceptorActionResume:
		rpcResp.Action = routerrpc.ResolveHoldForwardAction_RESUME

	default:
		return nil, fmt.Errorf("unknown action: %v", response.Action)
	}

	return rpcResp, nil
}

// MissionControlEntry contains a mission control entry for a node pair.
type MissionControlEntry struct {
	// NodeFrom is the node that the payment was forwarded from.
	NodeFrom route.Vertex

	// NodeTo is the node that the payment was forwarded to.
	NodeTo route.Vertex

	// FailTime is the time for our failed amount.
	FailTime time.Time

	// FailAmt is the payment amount that failed in millisatoshis.
	FailAmt lnwire.MilliSatoshi

	// SuccessTime is the time for our success amount.
	SuccessTime time.Time

	// SuccessAmt is the payment amount that succeeded in millisatoshis.
	SuccessAmt lnwire.MilliSatoshi
}

// QueryMissionControl will query Mission Control state from lnd.
func (r *routerClient) QueryMissionControl(ctx context.Context) (
	[]MissionControlEntry, error) {

	rpcCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req := &routerrpc.QueryMissionControlRequest{}
	res, err := r.client.QueryMissionControl(
		r.routerKitMac.WithMacaroonAuth(rpcCtx), req,
	)
	if err != nil {
		return nil, err
	}

	result := make([]MissionControlEntry, 0, len(res.Pairs))
	for _, pair := range res.Pairs {
		if pair.History == nil {
			continue
		}

		nodeFrom, err := route.NewVertexFromBytes(pair.NodeFrom)
		if err != nil {
			return nil, err
		}

		nodeTo, err := route.NewVertexFromBytes(pair.NodeTo)
		if err != nil {
			return nil, err
		}

		entry := MissionControlEntry{
			NodeFrom: nodeFrom,
			NodeTo:   nodeTo,
			FailAmt: lnwire.MilliSatoshi(
				pair.History.FailAmtMsat,
			),
			SuccessAmt: lnwire.MilliSatoshi(
				pair.History.SuccessAmtMsat,
			),
		}

		if pair.History.FailTime != 0 {
			entry.FailTime = time.Unix(pair.History.FailTime, 0)
		}

		if pair.History.SuccessTime != 0 {
			entry.SuccessTime = time.Unix(
				pair.History.SuccessTime, 0,
			)
		}

		result = append(result, entry)
	}

	return result, nil
}

// ImportMissionControl imports a set of pathfinding results to mission control.
// These results are not persisted across restarts.
func (r *routerClient) ImportMissionControl(ctx context.Context,
	entries []MissionControlEntry, force bool) error {

	rpcCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req := &routerrpc.XImportMissionControlRequest{
		Pairs: make([]*routerrpc.PairHistory, len(entries)),
		Force: force,
	}

	for i, entry := range entries {
		entry := entry
		rpcEntry := &routerrpc.PairHistory{
			NodeFrom: entry.NodeFrom[:],
			NodeTo:   entry.NodeTo[:],
			History: &routerrpc.PairData{
				SuccessAmtMsat: int64(entry.SuccessAmt),
				FailAmtMsat:    int64(entry.FailAmt),
			},
		}

		if !entry.FailTime.IsZero() {
			rpcEntry.History.FailTime = entry.FailTime.Unix()
		}

		if !entry.SuccessTime.IsZero() {
			rpcEntry.History.SuccessTime = entry.SuccessTime.Unix()
		}

		req.Pairs[i] = rpcEntry
	}

	_, err := r.client.XImportMissionControl(
		r.routerKitMac.WithMacaroonAuth(rpcCtx), req,
	)
	return err
}

// ResetMissionControl resets the Mission Control state of lnd.
func (r *routerClient) ResetMissionControl(ctx context.Context) error {
	rpcCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	_, err := r.client.ResetMissionControl(
		r.routerKitMac.WithMacaroonAuth(rpcCtx),
		&routerrpc.ResetMissionControlRequest{},
	)
	return err
}

// UpdateChanStatus attempts to manually set the state of a channel (enabled,
// disabled, or auto). A manual "disable" request will cause the channel to
// stay disabled until a subsequent manual request of either "enable" or "auto".
func (r *routerClient) UpdateChanStatus(ctx context.Context,
	channel *wire.OutPoint, action routerrpc.ChanStatusAction) error {

	rpcCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	_, err := r.client.UpdateChanStatus(
		r.routerKitMac.WithMacaroonAuth(rpcCtx),
		&routerrpc.UpdateChanStatusRequest{
			ChanPoint: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
					FundingTxidBytes: channel.Hash[:],
				},
				OutputIndex: channel.Index,
			},
			Action: action,
		},
	)
	return err
}
