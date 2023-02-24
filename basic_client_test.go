package lndclient

import (
	"encoding/hex"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests that NewBasicConn works correctly when macaroon and TLS certificate
// data are passed in directly instead of being supplied as file paths.
func TestParseTLSAndMacaroon(t *testing.T) {
	tlsData := `-----BEGIN CERTIFICATE-----
MIIDhzCCAm+gAwIBAgIUEkmdMOVPL92AwgsSYFFBvz4ilmUwDQYJKoZIhvcNAQEL
BQAwUzELMAkGA1UEBhMCVVMxCzAJBgNVBAgMAk1OMRQwEgYDVQQHDAtNaW5uZWFw
b2xpczEhMB8GA1UECgwYSW50ZXJuZXQgV2lkZ2l0cyBQdHkgTHRkMB4XDTIxMDQy
MzA2NDkyNVoXDTIxMDUyMzA2NDkyNVowUzELMAkGA1UEBhMCVVMxCzAJBgNVBAgM
Ak1OMRQwEgYDVQQHDAtNaW5uZWFwb2xpczEhMB8GA1UECgwYSW50ZXJuZXQgV2lk
Z2l0cyBQdHkgTHRkMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAnK21
qJmWWs4Nwz2f2ZbTsDxJAumgDJdZ9JKsJBrqjFf7+25ip+1hIB15P1UHHPhtW5Yp
P9Xm50z8W2RP2pHyCFB09cwKgdqPsS8Q2tzr5DINt+eNYa5JpxnWXM5ZqmYD7Zg0
wSMVW3FuAWFpjlzNWs/UHSuDShiQLoMhl2xAjiGSsHbY9plV438/kypSKS+7wjxe
0TJaTv/kWlHhQkXvnLqIMhD8J+ScGVSSk0OFgWiRmcCGDsLZgEGklHklC7ZKrr+Q
Am2MGbvUaGuwW+R5d2ZaQRbQ5UVhHcna2MxUn6MzSjbEhpIsMKZoYVXCb0GFObcq
UsLUOrIqpIyngd4G9wIDAQABo1MwUTAdBgNVHQ4EFgQU0lZJ2gp/RM79oAegXr/H
sU+GU3YwHwYDVR0jBBgwFoAU0lZJ2gp/RM79oAegXr/HsU+GU3YwDwYDVR0TAQH/
BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEAly744gq/LPuL0EnEbfxXrVqmvWh6
t9kNljXybVjQNTZ00e4zGknOA3VM29JWOEYyQ7ut/tP+kquWLdfOq/Lehe7vnBSn
lPR6IYbba9ck5AvPZgGG9fEncKxeUoI0ltI/luycmWL7Eb9j3128diIwljf9JXNT
I/LThs8Nl5RSiMOuGer0e934vLlZlrEEI4rWs3DKK56WjrMeVf5dhvYK44usNwUh
vKgMVFsUeyLLTN0EuZjGoFdi3lfLQo3vRwLD6h/EDa5uWK14pZXDQ30+fT2RjuVD
XhkpT5dliEGFLNe6OOgeWTU1JpEXfCud/GImtNMHQi4EDWQfvWuCNGhOoQ==
-----END CERTIFICATE-----`

	macData := "0201047465737402067788991234560000062052d26ed139ea5af8" +
		"3e675500c4ccb2471f62191b745bab820f129e5588a255d2"

	// Make sure it works when data is passed in.
	_, _, err := parseTLSAndMacaroon(
		"", "", "mainnet", MacFilename(""), TLSData(tlsData),
		MacaroonData(macData),
	)
	require.NoError(t, err)

	// Now let's write the data to a file to make sure parseTLSAndMacaroon
	// parses that properly as well.
	tempDirPath, err := ioutil.TempDir("", ".testCreds")
	require.NoError(t, err)
	defer os.RemoveAll(tempDirPath)

	certPath := tempDirPath + "/tls.cert"
	tlsPEMBytes := []byte(tlsData)

	err = ioutil.WriteFile(certPath, tlsPEMBytes, 0644)
	require.NoError(t, err)

	macPath := tempDirPath + "/test.macaroon"
	macBytes, err := hex.DecodeString(macData)
	require.NoError(t, err)

	err = ioutil.WriteFile(macPath, macBytes, 0644)
	require.NoError(t, err)

	_, _, err = parseTLSAndMacaroon(
		certPath, macPath, "mainnet", MacFilename(""),
	)
	require.NoError(t, err)
}
