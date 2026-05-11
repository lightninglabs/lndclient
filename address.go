package lndclient

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/lightningnetwork/lnd/tor"
)

func parseNetwork(addr net.Addr) string {
	switch addr := addr.(type) {
	case *net.TCPAddr:
		if addr.IP.To4() != nil {
			return "tcp4"
		}

		return "tcp6"

	default:
		return addr.Network()
	}
}

func isLoopback(host string) bool {
	if strings.Contains(host, "localhost") {
		return true
	}

	rawHost, _, _ := net.SplitHostPort(host)
	addr := net.ParseIP(rawHost)
	if addr == nil {
		return false
	}

	return addr.IsLoopback()
}

func isIPv6Host(host string) bool {
	v6Addr := net.ParseIP(host)
	if v6Addr == nil {
		return false
	}

	return v6Addr.To4() == nil
}

func isUnspecifiedHost(host string) bool {
	addr := net.ParseIP(host)
	if addr == nil {
		return false
	}

	return addr.IsUnspecified()
}

func verifyPort(address string, defaultPort string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if _, err := strconv.Atoi(address); err == nil {
			return net.JoinHostPort("localhost", address)
		}

		if strings.HasPrefix(address, "[") {
			return address + ":" + defaultPort
		}

		return net.JoinHostPort(address, defaultPort)
	}

	if host == "" && port == "" {
		return ":" + defaultPort
	}

	return address
}

func parseAddressString(strAddress string, defaultPort string,
	tcpResolver func(network, addr string) (*net.TCPAddr, error)) (
	net.Addr, error) {

	var parsedNetwork, parsedAddr string

	if strings.Contains(strAddress, "://") {
		parts := strings.Split(strAddress, "://")
		parsedNetwork, parsedAddr = parts[0], parts[1]
	} else if strings.Contains(strAddress, ":") {
		parts := strings.Split(strAddress, ":")
		parsedNetwork = parts[0]
		parsedAddr = strings.Join(parts[1:], ":")
	}

	switch parsedNetwork {
	case "unix", "unixpacket":
		return net.ResolveUnixAddr(parsedNetwork, parsedAddr)

	case "tcp", "tcp4", "tcp6":
		return tcpResolver(
			parsedNetwork, verifyPort(parsedAddr, defaultPort),
		)

	default:
		addrWithPort := verifyPort(strAddress, defaultPort)
		rawHost, rawPort, _ := net.SplitHostPort(addrWithPort)

		if tor.IsOnionHost(rawHost) {
			portNum, err := strconv.Atoi(rawPort)
			if err != nil {
				return nil, err
			}

			return &tor.OnionAddr{
				OnionService: rawHost,
				Port:         portNum,
			}, nil
		}

		if rawHost == "" || isLoopback(rawHost) ||
			isIPv6Host(rawHost) || isUnspecifiedHost(rawHost) {

			return net.ResolveTCPAddr("tcp", addrWithPort)
		}

		addr, err := tcpResolver("tcp", addrWithPort)
		if err != nil {
			torErrStr := "tor host is unreachable"
			if strings.Contains(err.Error(), torErrStr) {
				return net.ResolveTCPAddr("tcp", addrWithPort)
			}

			return nil, err
		}

		return addr, nil
	}
}

// ClientAddressDialer creates a gRPC dialer that can also dial unix socket
// addresses instead of just TCP.
func ClientAddressDialer(defaultPort string) func(context.Context,
	string) (net.Conn, error) {

	return func(ctx context.Context, addr string) (net.Conn, error) {
		parsedAddr, err := parseAddressString(
			addr, defaultPort, net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}

		d := net.Dialer{}
		return d.DialContext(
			ctx, parseNetwork(parsedAddr), parsedAddr.String(),
		)
	}
}
