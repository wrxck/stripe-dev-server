package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wrxck/stripe-dev-server/internal/store"
)

func TestProxyCapturesRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"pi_123","amount":1000}`)
	}))
	defer upstream.Close()

	st := store.New(0)
	h, err := New(Config{UpstreamURL: upstream.URL}, st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/v1/payment_intents?expand[]=customer",
		"application/x-www-form-urlencoded",
		strings.NewReader("amount=1000&currency=usd"),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "pi_123") {
		t.Fatalf("body missing pi_123: %s", body)
	}

	captures := st.All("", 0)
	if len(captures) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(captures))
	}
	c := captures[0]
	if c.Method != "POST" {
		t.Errorf("method = %q", c.Method)
	}
	if c.Path != "/v1/payment_intents" {
		t.Errorf("path = %q", c.Path)
	}
	if c.Query != "expand[]=customer" {
		t.Errorf("query = %q", c.Query)
	}
	if !strings.Contains(c.RequestBody, "amount=1000") {
		t.Errorf("requestBody = %q", c.RequestBody)
	}
	if c.Status != http.StatusCreated {
		t.Errorf("status = %d", c.Status)
	}
	if !strings.Contains(c.ResponseBody, "pi_123") {
		t.Errorf("responseBody = %q", c.ResponseBody)
	}
	if c.ResponseHeaders["X-Test"] != "yes" {
		t.Errorf("response header X-Test = %q", c.ResponseHeaders["X-Test"])
	}
}

func TestProxyHandlesGetWithoutBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer upstream.Close()
	st := store.New(0)
	h, _ := New(Config{UpstreamURL: upstream.URL}, st)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/customers")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if st.Count() != 1 {
		t.Fatalf("expected 1 capture")
	}
}

func TestInvalidUpstreamURLErrors(t *testing.T) {
	_, err := New(Config{UpstreamURL: "://nope"}, store.New(0))
	if err == nil {
		t.Fatalf("expected error on invalid URL")
	}
}
