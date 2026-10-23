// Package targetpolicy validates and dials webhook targets without allowing
// requests to local or otherwise non-public network addresses.
package targetpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var ErrUnsafeTarget = errors.New("url must be an absolute http(s) URL with a publicly routable target")

// LookupIP resolves a hostname to its IP addresses. It is injectable so the
// policy can be tested without depending on an external DNS service.
type LookupIP func(context.Context, string) ([]net.IP, error)

// Validator applies PayRelay's outbound target policy at registration and at
// connection time. Both checks are necessary to defend against DNS rebinding.
type Validator struct {
	lookupIP LookupIP
	dialer   net.Dialer
}

func NewValidator() *Validator {
	return NewValidatorWithLookup(func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	})
}

func NewValidatorWithLookup(lookupIP LookupIP) *Validator {
	if lookupIP == nil {
		lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	return &Validator{lookupIP: lookupIP}
}

// Validate checks that rawURL is an absolute HTTP(S) URL without credentials
// and that every current DNS answer is publicly routable.
func (v *Validator) Validate(ctx context.Context, rawURL string) error {
	target, err := parse(rawURL)
	if err != nil {
		return ErrUnsafeTarget
	}
	_, err = v.publicIPs(ctx, target.Hostname())
	return err
}

// DialContext resolves address itself, validates every answer, and dials the
// approved IP address directly. http.Transport uses it at delivery time.
func (v *Validator) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return nil, ErrUnsafeTarget
	}
	ips, err := v.publicIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		connection, err := v.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrUnsafeTarget
}

func parse(rawURL string) (*url.URL, error) {
	target, err := url.ParseRequestURI(rawURL)
	if err != nil || target.Scheme != "http" && target.Scheme != "https" || target.Host == "" || target.User != nil {
		return nil, ErrUnsafeTarget
	}
	if port := target.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return nil, ErrUnsafeTarget
		}
	}
	return target, nil
}

func (v *Validator) publicIPs(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || host == "localhost" {
		return nil, ErrUnsafeTarget
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublic(ip) {
			return nil, ErrUnsafeTarget
		}
		return []net.IP{ip}, nil
	}
	addresses, err := v.lookupIP(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrUnsafeTarget
	}
	for _, address := range addresses {
		if !isPublic(address) {
			return nil, ErrUnsafeTarget
		}
	}
	return addresses, nil
}

func isPublic(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}
