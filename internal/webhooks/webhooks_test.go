package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSignProducesStripeFormat(t *testing.T) {
	payload := []byte(`{"id":"evt_test"}`)
	ts := int64(1700000000)
	secret := "whsec_test"

	got := Sign(payload, ts, secret)

	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	want := fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))

	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

func TestEventEnvelope(t *testing.T) {
	data := json.RawMessage(`{"id":"pi_123","amount":1000}`)
	out := Event("evt_fixed", "payment_intent.succeeded", data)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["id"] != "evt_fixed" {
		t.Errorf("id = %v", got["id"])
	}
	if got["type"] != "payment_intent.succeeded" {
		t.Errorf("type = %v", got["type"])
	}
	if got["object"] != "event" {
		t.Errorf("object = %v", got["object"])
	}
	dataMap, _ := got["data"].(map[string]any)
	obj, _ := dataMap["object"].(map[string]any)
	if obj["id"] != "pi_123" {
		t.Errorf("data.object.id = %v", obj["id"])
	}
}

func TestDispatchPostsSignedEnvelope(t *testing.T) {
	var got struct {
		called    int32
		signature string
		bodyType  string
		eventType string
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&got.called, 1)
		got.signature = r.Header.Get("Stripe-Signature")
		got.bodyType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		var env map[string]any
		_ = json.Unmarshal(body, &env)
		if et, ok := env["type"].(string); ok {
			got.eventType = et
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()

	status, body, err := Dispatch(nil, target.URL, "whsec_test", "customer.created", json.RawMessage(`{"id":"cus_1"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
	if body != "ok" {
		t.Errorf("body = %q", body)
	}
	if atomic.LoadInt32(&got.called) != 1 {
		t.Fatalf("target not called")
	}
	if !strings.HasPrefix(got.signature, "t=") || !strings.Contains(got.signature, ",v1=") {
		t.Errorf("signature shape wrong: %q", got.signature)
	}
	if got.eventType != "customer.created" {
		t.Errorf("eventType = %q", got.eventType)
	}
	if got.bodyType != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", got.bodyType)
	}
}

func TestDispatchReturnsNetworkError(t *testing.T) {
	_, _, err := Dispatch(&http.Client{Timeout: 100 * time.Millisecond}, "http://127.0.0.1:1", "x", "y", nil)
	if err == nil {
		t.Fatalf("expected error on dead target")
	}
}
