package webhook

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/payrelay/internal/core/domain"
	"github.com/example/payrelay/internal/targetpolicy"
)

func TestHTTPSender_GivenReceiver_WhenSending_ThenPostsSignedEnvelope(t *testing.T) {
	var receivedBody []byte
	var receivedSignature, receivedTimestamp string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedSignature = r.Header.Get("X-PayRelay-Signature")
		receivedTimestamp = r.Header.Get("X-PayRelay-Timestamp")
		if r.Method != http.MethodPost || r.Header.Get("X-PayRelay-Event-ID") != "evt_1" {
			t.Errorf("unexpected request: method=%s event=%q", r.Method, r.Header.Get("X-PayRelay-Event-ID"))
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	sender := NewHTTPSender(receiver.Client())
	result, err := sender.Send(context.Background(), domain.Endpoint{URL: receiver.URL, Secret: "whsec_test"}, domain.PaymentEvent{ID: "evt_1", Type: "payment.succeeded"})
	if err != nil || result.StatusCode != http.StatusAccepted || result.Duration < 0 {
		t.Fatalf("send result=%#v err=%v", result, err)
	}
	if receivedSignature == "" || receivedTimestamp == "" || len(receivedBody) == 0 {
		t.Fatal("receiver did not observe a complete signed request")
	}
}

func TestHTTPSender_GivenTransportOrURLFailure_WhenSending_ThenReturnsError(t *testing.T) {
	cases := []struct {
		name, url string
		client    *http.Client
	}{
		{name: "given_offline_transport_when_sending_then_returns_error", url: "https://receiver.test", client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}},
		{name: "given_invalid_url_when_sending_then_returns_error", url: ":// invalid", client: http.DefaultClient},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NewHTTPSender(tt.client).Send(context.Background(), domain.Endpoint{URL: tt.url, Secret: "whsec_test"}, domain.PaymentEvent{ID: "evt_1"})
			if err == nil || (strings.Contains(tt.name, "offline") && !strings.Contains(err.Error(), "offline")) || result.StatusCode != 0 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestNewSafeHTTPClient_GivenLoopbackTarget_WhenRequesting_ThenItRejectsTheConnection(t *testing.T) {
	client := NewSafeHTTPClient(time.Second)
	if _, err := client.Get("http://127.0.0.1:8080/internal"); err == nil {
		t.Fatal("Get() error = nil, want loopback target rejection")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, targetpolicy.ErrUnsafeTarget) {
		t.Fatalf("CheckRedirect() error = %v, want %v", err, targetpolicy.ErrUnsafeTarget)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
