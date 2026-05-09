// Package webhooks dispatches synthetic Stripe webhook events with a valid
// signature so an application can exercise its webhook receiver without
// involving the real Stripe API.
//
// The signature scheme follows Stripe's documented format:
//
//	t=<unix-seconds>,v1=<HMAC-SHA256(secret, "<unix-seconds>.<payload>")>
//
// See https://stripe.com/docs/webhooks/signatures.
package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sign returns the value for Stripe's `Stripe-Signature` header for the
// given payload + UNIX timestamp + secret.
func Sign(payload []byte, ts int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// Event constructs a Stripe-shaped event JSON envelope.
func Event(eventID, eventType string, dataObject json.RawMessage) []byte {
	if eventID == "" {
		eventID = "evt_" + randomID(24)
	}
	if dataObject == nil || len(dataObject) == 0 {
		dataObject = json.RawMessage(`{}`)
	}
	envelope := map[string]any{
		"id":               eventID,
		"object":           "event",
		"api_version":      "2024-06-20",
		"created":          time.Now().Unix(),
		"livemode":         false,
		"type":             eventType,
		"pending_webhooks": 1,
		"request":          map[string]any{"id": nil, "idempotency_key": nil},
		"data": map[string]any{
			"object": json.RawMessage(dataObject),
		},
	}
	out, _ := json.Marshal(envelope)
	return out
}

// Dispatch builds a signed Stripe-shaped event and POSTs it to targetURL.
// Returns the upstream response status and a snippet of the body.
func Dispatch(client *http.Client, targetURL, secret, eventType string, data json.RawMessage) (status int, bodySnippet string, err error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	payload := Event("", eventType, data)
	ts := time.Now().Unix()
	sig := Sign(payload, ts, secret)

	req, _ := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Stripe-Signature", sig)
	req.Header.Set("User-Agent", "stripe-dev-server/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return resp.StatusCode, strings.TrimSpace(string(body)), nil
}

func randomID(n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[time.Now().UnixNano()%int64(len(alpha))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
