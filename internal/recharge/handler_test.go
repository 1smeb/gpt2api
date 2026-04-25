package recharge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEPayCallbackValuesParsesRawBodyWithoutContentType(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/public/epay/notify?money=0.01", strings.NewReader("out_trade_no=o123&money=12.00&sign=abc"))

	values, err := epayCallbackValues(r)
	if err != nil {
		t.Fatalf("epayCallbackValues returned error: %v", err)
	}
	if got := values.Get("out_trade_no"); got != "o123" {
		t.Fatalf("out_trade_no = %q, want o123", got)
	}
	if got := values.Get("money"); got != "12.00" {
		t.Fatalf("money = %q, want body value 12.00", got)
	}
	if got := values.Get("sign"); got != "abc" {
		t.Fatalf("sign = %q, want abc", got)
	}
}

func TestRequestBaseURLPrefersForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://internal.local/api/recharge/orders", nil)
	r.Host = "internal.local"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "pay.example.com")

	if got := requestBaseURL(r); got != "https://pay.example.com" {
		t.Fatalf("requestBaseURL = %q, want https://pay.example.com", got)
	}
}
