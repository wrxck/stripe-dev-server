// Package proxy implements a small reverse proxy in front of stripe-mock
// that captures the full request/response of every passing call.
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/wrxck/stripe-dev-server/internal/store"
)

// Config configures the proxy.
type Config struct {
	UpstreamURL  string // e.g. "http://127.0.0.1:12111"
	MaxBodyBytes int64  // request/response body capture cap (default 1 MiB)
}

// New returns an http.Handler that proxies to the upstream and captures
// every round-trip into the store.
func New(cfg Config, st *store.Store) (http.Handler, error) {
	target, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(*http.Response) error { return nil } // placeholder

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Capture the request body (up to cap), then restore it for upstream.
		reqBody, _ := io.ReadAll(io.LimitReader(r.Body, cfg.MaxBodyBytes))
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(reqBody))

		// Wrap the response writer so we can capture status + body.
		rec := &recorder{ResponseWriter: w, status: 200, body: &bytes.Buffer{}, max: cfg.MaxBodyBytes}

		rp.ServeHTTP(rec, r)

		st.Add(&store.Capture{
			Method:          r.Method,
			Path:            r.URL.Path,
			Query:           r.URL.RawQuery,
			RequestHeaders:  flattenHeaders(r.Header),
			RequestBody:     string(reqBody),
			Status:          rec.status,
			ResponseHeaders: flattenHeaders(rec.Header()),
			ResponseBody:    rec.body.String(),
			DurationMs:      time.Since(start).Milliseconds(),
		})
	}), nil
}

// recorder wraps http.ResponseWriter and captures the body up to a cap.
type recorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
	max    int64
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if int64(r.body.Len()) < r.max {
		room := r.max - int64(r.body.Len())
		if int64(len(b)) <= room {
			r.body.Write(b)
		} else {
			r.body.Write(b[:room])
		}
	}
	return r.ResponseWriter.Write(b)
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}
