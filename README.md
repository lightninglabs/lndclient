# IBEX fork of lightninglabs/lndclient

Changes that were introduced are required by the following services

```
btcproxy:
- NewAddress grpc call
- SendMany grpc call
- EstimateFee grpc call

lnproxy:
- invoiceUpdate millisatoshi support
- PaymentAddr and TimePref support for SendPaymentRequest
- TimePref support for QueryRoutesRequest

node-metrics-agent:
- PeerAliasLookup support for ListChannels grpc call
- PeerAlias support for ChannelInfo
```

## Building a new release

Tag the commit and push the tag to remote. Then simply import the release in the target release.

Increment the last digit for each new tag.

```
# lndclient
git tag ibex-v0.17.0-0
git push origin ibex-v0.17.0-0

# target service
replace ...lndclient... => ...lndclient ibex-v0.17.0-0
go mod tidy -v
```