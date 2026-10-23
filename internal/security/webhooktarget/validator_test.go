package targetpolicy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestValidator_GivenEndpointURL_WhenValidating_ThenAllowsOnlyPublicTargets(t *testing.T) {
	resolver := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "public.example.test":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "mixed.example.test":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.8")}, nil
		case "private.example.test":
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		default:
			return nil, errors.New("not found")
		}
	}
	validator := NewValidatorWithLookup(resolver)
	cases := []struct {
		name string
		url  string
		want error
	}{
		{name: "given public hostname when validating then it is allowed", url: "https://public.example.test/webhooks/payrelay"},
		{name: "given public IPv4 when validating then it is allowed", url: "https://8.8.8.8/webhooks/payrelay"},
		{name: "given loopback address when validating then it is rejected", url: "http://127.0.0.1:8080/hooks", want: ErrUnsafeTarget},
		{name: "given private address when validating then it is rejected", url: "http://10.0.0.8/hooks", want: ErrUnsafeTarget},
		{name: "given link local address when validating then it is rejected", url: "http://169.254.169.254/latest/meta-data", want: ErrUnsafeTarget},
		{name: "given IPv6 loopback when validating then it is rejected", url: "http://[::1]/hooks", want: ErrUnsafeTarget},
		{name: "given localhost hostname when validating then it is rejected", url: "http://localhost:8080/hooks", want: ErrUnsafeTarget},
		{name: "given mixed DNS answers when validating then it is rejected", url: "https://mixed.example.test/hooks", want: ErrUnsafeTarget},
		{name: "given private DNS answer when validating then it is rejected", url: "https://private.example.test/hooks", want: ErrUnsafeTarget},
		{name: "given URL credentials when validating then it is rejected", url: "https://user:pass@public.example.test/hooks", want: ErrUnsafeTarget},
		{name: "given non HTTP scheme when validating then it is rejected", url: "file:///tmp/webhook", want: ErrUnsafeTarget},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(context.Background(), tt.url)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidator_GivenUnsafeAddress_WhenDialing_ThenItIsRejectedBeforeConnecting(t *testing.T) {
	validator := NewValidatorWithLookup(nil)
	cases := []struct {
		name    string
		address string
		want    error
	}{
		{name: "given malformed address when dialing then it is rejected", address: "not-an-address", want: ErrUnsafeTarget},
		{name: "given loopback address when dialing then it is rejected", address: "127.0.0.1:8080", want: ErrUnsafeTarget},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validator.DialContext(context.Background(), "tcp", tt.address); !errors.Is(err, tt.want) {
				t.Fatalf("DialContext() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidator_GivenPublicAddress_WhenDialingFails_ThenItReturnsTheConnectionError(t *testing.T) {
	validator := NewValidatorWithLookup(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := validator.DialContext(ctx, "tcp", "public.example.test:1"); err == nil {
		t.Fatal("DialContext() error = nil, want a connection error")
	}
}

func TestValidator_GivenDefaultResolver_WhenValidatingPublicLiteral_ThenItDoesNotNeedDNS(t *testing.T) {
	if err := NewValidator().Validate(context.Background(), "https://8.8.8.8/hooks"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
