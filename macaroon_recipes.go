package lndclient

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// recipeMethod identifies a method on one of lndclient's public client
// interfaces.
type recipeMethod struct {
	pkg    string
	method string
}

// rpcMethod identifies a concrete lnd RPC method.
type rpcMethod struct {
	pkg     string
	service string
	method  string
}

// uri returns the full gRPC method URI for the RPC method.
func (r rpcMethod) uri() string {
	return fmt.Sprintf("/%s.%s/%s", r.pkg, r.service, r.method)
}

var (
	// supportedSubservers is a map of all RPC (sub)server names that are
	// supported by the lndclient library and their implementing interface
	// type. We use reflection to look up the methods implemented on those
	// interfaces to find out which permissions are needed for them.
	supportedSubservers = map[string]interface{}{
		"lnrpc":       (*LightningClient)(nil),
		"chainrpc":    (*ChainNotifierClient)(nil),
		"invoicesrpc": (*InvoicesClient)(nil),
		"routerrpc":   (*RouterClient)(nil),
		"signrpc":     (*SignerClient)(nil),
		"verrpc":      (*VersionerClient)(nil),
		"walletrpc":   (*WalletKitClient)(nil),
		"wtclientrpc": (*WatchtowerClientClient)(nil),
	}

	// renames is a map of renamed RPC method names. The key is the name as
	// implemented in lndclient and the value is the original name of the
	// RPC method defined in the proto.
	renames = map[string]string{
		"ChannelBackup":             "ExportChannelBackup",
		"ChannelBackups":            "ExportAllChannelBackups",
		"ConfirmedWalletBalance":    "WalletBalance",
		"Connect":                   "ConnectPeer",
		"DecodePaymentRequest":      "DecodePayReq",
		"ListTransactions":          "GetTransactions",
		"UpdateChanPolicy":          "UpdateChannelPolicy",
		"NetworkInfo":               "GetNetworkInfo",
		"SubscribeGraph":            "SubscribeChannelGraph",
		"InterceptHtlcs":            "HtlcInterceptor",
		"ImportMissionControl":      "XImportMissionControl",
		"EstimateFeeRate":           "EstimateFee",
		"EstimateFeeToP2WSH":        "EstimateFee",
		"EstimateRouteFeeWithProbe": "EstimateRouteFee",
		"OpenChannelStream":         "OpenChannel",
		"ListSweepsVerbose":         "ListSweeps",
		"MinRelayFee":               "EstimateFee",
		"SignOutputRawKeyLocator":   "SignOutputRaw",
	}

	// methodOverrides maps lndclient methods to their exact backing RPC
	// methods when a method is implemented through another subserver or
	// multiple RPCs. The key uses the lndclient package and method name,
	// not the proto method name.
	methodOverrides = map[recipeMethod][]rpcMethod{
		{"lnrpc", "PayInvoice"}: {
			{"routerrpc", "Router", "SendPaymentV2"},
			{"routerrpc", "Router", "TrackPaymentV2"},
		},
	}

	// ignores is a list of method names on the client implementations that
	// we don't need to check macaroon permissions for.
	ignores = []string{
		"RawClientWithMacAuth",
	}
)

// MacaroonRecipe returns a list of macaroon permissions that is required to use
// the full feature set of the given list of RPC package names.
func MacaroonRecipe(c LightningClient, packages []string) ([]MacaroonPermission,
	error) {

	// Get the full map of RPC URIs and the required permissions from the
	// backing lnd instance.
	allPermissions, err := c.ListPermissions(context.Background())
	if err != nil {
		return nil, err
	}

	uniquePermissions := make(map[string]map[string]struct{})
	for _, pkg := range packages {
		// Get the typed pointer from our map of supported interfaces.
		ifacePtr, ok := supportedSubservers[pkg]
		if !ok {
			return nil, fmt.Errorf("unknown subserver %s", pkg)
		}

		// From the pointer type we can find out the interface, its name
		// and what methods it declares.
		ifaceType := reflect.TypeOf(ifacePtr).Elem()
		serverName := strings.TrimSuffix(ifaceType.Name(), "Client")
		for i := range ifaceType.NumMethod() {
			// The methods in lndclient might be called slightly
			// differently. Rename according to our rename mapping
			// table.
			clientMethodName := ifaceType.Method(i).Name
			methodName := clientMethodName
			rename, ok := renames[methodName]
			if ok {
				methodName = rename
			}

			if slices.Contains(ignores, methodName) {
				continue
			}

			method := recipeMethod{
				pkg:    pkg,
				method: clientMethodName,
			}
			rpcMethods, ok := methodOverrides[method]
			if !ok {
				rpcMethods = []rpcMethod{
					{
						pkg:     pkg,
						service: serverName,
						method:  methodName,
					},
				}
			}

			for _, rpcMethod := range rpcMethods {
				rpcURI := rpcMethod.uri()
				requiredPermissions, ok := allPermissions[rpcURI]
				if !ok {
					return nil, fmt.Errorf("URI %s not found "+
						"in permission list", rpcURI)
				}

				// Add these permissions to the map we use to
				// de-duplicate the values.
				for _, perm := range requiredPermissions {
					actions, ok := uniquePermissions[perm.Entity]
					if !ok {
						actions = make(map[string]struct{})
						uniquePermissions[perm.Entity] = actions
					}
					actions[perm.Action] = struct{}{}
				}
			}
		}
	}

	// Turn the de-duplicated map back into a slice of permission entries.
	var requiredPermissions []MacaroonPermission
	for entity, actions := range uniquePermissions {
		for action := range actions {
			requiredPermissions = append(
				requiredPermissions, MacaroonPermission{
					Entity: entity,
					Action: action,
				},
			)
		}
	}
	return requiredPermissions, nil
}
