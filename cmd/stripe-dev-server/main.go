// stripe-dev-server is a developer tool that reverse-proxies Stripe API
// calls to a local stripe-mock instance, captures every request/response
// pair, exposes a dark-mode inspection UI, and ships an MCP server so an
// LLM client can query captured traffic and trigger fake webhooks.
//
// Run modes:
//
//	stripe-dev-server                       # default: proxy + UI
//	stripe-dev-server mcp [--upstream URL]  # stdio MCP server
//	stripe-dev-server version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wrxck/stripe-dev-server/internal/mcp"
	"github.com/wrxck/stripe-dev-server/internal/proxy"
	"github.com/wrxck/stripe-dev-server/internal/store"
	"github.com/wrxck/stripe-dev-server/internal/ui"
	"github.com/wrxck/stripe-dev-server/internal/webhooks"
)

var version = "dev"

const banner = `stripe-dev-server %s
A development reverse proxy for Stripe (powered by stripe-mock).
Proxy: http://%s   ·   UI/inspect: http://%s
`

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "mcp":
			os.Exit(runMCP(os.Args[2:]))
		case "version", "--version", "-v":
			fmt.Printf("stripe-dev-server %s\n", version)
			return
		case "help", "--help", "-h":
			usage()
			return
		}
	}
	os.Exit(runDefault(os.Args[1:]))
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  stripe-dev-server                  Run proxy + UI (auto-spawns stripe-mock)
  stripe-dev-server mcp [--upstream] Run MCP server over stdio
  stripe-dev-server version          Show version
`)
}

func runDefault(args []string) int {
	fs := flag.NewFlagSet("default", flag.ExitOnError)
	proxyAddr := fs.String("proxy", envOr("PROXY_ADDR", "127.0.0.1:12112"), "Address that forwards Stripe API calls to stripe-mock")
	uiAddr := fs.String("ui", envOr("UI_ADDR", "127.0.0.1:12113"), "Address that serves the inspection UI + /_dev/* API")
	stripeMockAddr := fs.String("stripe-mock", envOr("STRIPE_MOCK_ADDR", "127.0.0.1:12111"), "Address of the upstream stripe-mock to spawn / connect to")
	stripeMockBin := fs.String("stripe-mock-bin", envOr("STRIPE_MOCK_BIN", ""), "Path to stripe-mock binary (default: discover on PATH or $GOPATH/bin)")
	skipSpawn := fs.Bool("no-spawn", false, "Don't spawn stripe-mock; assume it's already running at --stripe-mock")
	webhookSecret := fs.String("webhook-secret", envOr("WEBHOOK_SECRET", "whsec_dev_local_secret"), "Secret used to sign synthetic webhook events")
	maxItems := fs.Int("max-items", 1000, "Max captures retained in memory")
	_ = fs.Parse(args)

	st := store.New(*maxItems)

	// Spawn stripe-mock unless told not to.
	var mockProc *exec.Cmd
	if !*skipSpawn {
		bin, err := findStripeMock(*stripeMockBin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stripe-mock not found. Install with:")
			fmt.Fprintln(os.Stderr, "  go install github.com/stripe/stripe-mock@latest")
			fmt.Fprintln(os.Stderr, "or pass --stripe-mock-bin <path> or --no-spawn.")
			fmt.Fprintln(os.Stderr, "Detail:", err)
			return 1
		}
		// Disable stripe-mock's HTTPS listener (-https-port -1) so we can
		// freely choose any port for the proxy without colliding with
		// stripe-mock's default HTTPS port 12112.
		mockProc = exec.Command(bin, "-http-addr", *stripeMockAddr, "-https-port", "-1")
		mockProc.Stdout = os.Stdout
		mockProc.Stderr = os.Stderr
		if err := mockProc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "spawn stripe-mock:", err)
			return 1
		}
		defer func() {
			_ = mockProc.Process.Signal(syscall.SIGTERM)
			_ = mockProc.Wait()
		}()
		// Give it a beat to bind.
		if err := waitForListener("http://"+*stripeMockAddr, 5*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "stripe-mock did not become ready:", err)
			return 1
		}
	}

	// Build the capturing proxy.
	proxyHandler, err := proxy.New(proxy.Config{UpstreamURL: "http://" + *stripeMockAddr}, st)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proxy:", err)
		return 1
	}
	proxySrv := &http.Server{
		Addr:              *proxyAddr,
		Handler:           proxyHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Build the inspection / UI server.
	uiMux := http.NewServeMux()
	uiMux.Handle("/", ui.Handler())
	uiMux.HandleFunc("/_dev/captures", devCapturesList(st))
	uiMux.HandleFunc("/_dev/captures/", devCapturesGet(st))
	uiMux.HandleFunc("/_dev/webhooks/trigger", devWebhookTrigger(*webhookSecret))
	uiMux.HandleFunc("/_dev/status", devStatus(st, *proxyAddr, *uiAddr, *stripeMockAddr, mockProc != nil))
	uiSrv := &http.Server{
		Addr:              *uiAddr,
		Handler:           uiMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf(banner, version, *proxyAddr, *uiAddr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- proxySrv.ListenAndServe() }()
	go func() { errCh <- uiSrv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "http error:", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = proxySrv.Shutdown(shutdownCtx)
		_ = uiSrv.Shutdown(shutdownCtx)
		return 0
	}
}

func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	upstream := fs.String("upstream", envOr("MCP_UPSTREAM", "http://127.0.0.1:12113"), "Base URL of a running stripe-dev-server UI")
	_ = fs.Parse(args)
	if err := mcp.Run(context.Background(), os.Stdin, os.Stdout, *upstream); err != nil {
		fmt.Fprintln(os.Stderr, "mcp error:", err)
		return 1
	}
	return 0
}

// ----------------------------------------------------------------------------
// /_dev/* handlers
// ----------------------------------------------------------------------------

func devCapturesList(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pathFilter := r.URL.Query().Get("path")
			limit := 0
			if l := r.URL.Query().Get("limit"); l != "" {
				_, _ = fmt.Sscanf(l, "%d", &limit)
			}
			writeJSON(w, http.StatusOK, st.All(pathFilter, limit))
		case http.MethodDelete:
			st.Clear()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func devCapturesGet(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/_dev/captures/")
		if id == "" {
			http.NotFound(w, r)
			return
		}
		c := st.ByID(id)
		if c == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

func devWebhookTrigger(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			EventType  string          `json:"eventType"`
			TargetURL  string          `json:"targetUrl"`
			DataObject json.RawMessage `json:"dataObject"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.EventType == "" || req.TargetURL == "" {
			http.Error(w, "eventType and targetUrl required", http.StatusBadRequest)
			return
		}
		status, body, err := webhooks.Dispatch(nil, req.TargetURL, secret, req.EventType, req.DataObject)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"targetStatus": status,
			"targetBody":   body,
			"eventType":    req.EventType,
		})
	}
}

func devStatus(st *store.Store, proxyAddr, uiAddr, mockAddr string, spawned bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"capturedCount":  st.Count(),
			"proxyAddr":      proxyAddr,
			"uiAddr":         uiAddr,
			"stripeMockAddr": mockAddr,
			"spawnedMock":    spawned,
			"now":            time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// findStripeMock locates a stripe-mock binary, preferring the user-specified
// path, then $PATH, then $GOPATH/bin.
func findStripeMock(override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
	}
	if p, err := exec.LookPath("stripe-mock"); err == nil {
		return p, nil
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		candidate := filepath.Join(gp, "bin", "stripe-mock")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "stripe-mock")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("not on PATH or in $GOPATH/bin")
}

func waitForListener(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// guard against unused-import drift
var _ = io.Copy
