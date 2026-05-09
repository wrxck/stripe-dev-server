// Package mcp provides a minimal stdio JSON-RPC 2.0 implementation of the
// Model Context Protocol so that an LLM client (e.g. Claude Code) can
// inspect captured Stripe traffic and trigger fake webhooks against a
// running stripe-dev-server.
//
// Start it with:
//
//	stripe-dev-server mcp --upstream http://127.0.0.1:12112
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Run reads JSON-RPC requests from r and writes responses to w.
func Run(ctx context.Context, r io.Reader, w io.Writer, upstream string) error {
	if upstream == "" {
		upstream = "http://127.0.0.1:12112"
	}
	if _, err := url.Parse(upstream); err != nil {
		return fmt.Errorf("invalid upstream URL %q: %w", upstream, err)
	}
	upstream = strings.TrimRight(upstream, "/")

	srv := &server{upstream: upstream, http: &http.Client{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		resp := srv.handle(ctx, req)
		if req.ID == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type server struct {
	upstream string
	http     *http.Client
}

func (s *server) handle(ctx context.Context, req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "stripe-dev-server", "version": "0.1.0"},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": tools()}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
			return resp
		}
		out, err := s.callTool(ctx, p.Name, p.Arguments)
		if err != nil {
			resp.Result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "error: " + err.Error()}},
				"isError": true,
			}
			return resp
		}
		text, _ := json.MarshalIndent(out, "", "  ")
		resp.Result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_captures",
			"description": "Returns Stripe API calls captured by the running stripe-dev-server (newest first). Optional path substring filter and result limit.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter_path": map[string]any{"type": "string", "description": "Substring to match against the request path"},
					"limit":       map[string]any{"type": "integer", "description": "Max captures to return"},
				},
			},
		},
		{
			"name":        "get_capture",
			"description": "Returns the full request/response for the given capture id.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"id"},
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
			},
		},
		{
			"name":        "clear_captures",
			"description": "Drops all captured Stripe API calls from the in-memory store.",
			"inputSchema": map[string]any{"type": "object"},
		},
		{
			"name":        "trigger_webhook",
			"description": "Constructs a Stripe-shaped webhook event with a valid signature (using the configured webhook secret) and POSTs it to the target URL. Returns the upstream status + response body.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"event_type", "target_url"},
				"properties": map[string]any{
					"event_type":  map[string]any{"type": "string", "description": "e.g. payment_intent.succeeded"},
					"target_url":  map[string]any{"type": "string", "description": "Receiver URL"},
					"data_object": map[string]any{"type": "object", "description": "Payload for data.object"},
				},
			},
		},
		{
			"name":        "get_server_status",
			"description": "Returns ports, capture count, stripe-mock subprocess status.",
			"inputSchema": map[string]any{"type": "object"},
		},
	}
}

func (s *server) callTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "list_captures":
		var a struct {
			FilterPath string `json:"filter_path"`
			Limit      int    `json:"limit"`
		}
		_ = json.Unmarshal(args, &a)
		q := url.Values{}
		if a.FilterPath != "" {
			q.Set("path", a.FilterPath)
		}
		if a.Limit > 0 {
			q.Set("limit", strconv.Itoa(a.Limit))
		}
		path := "/_dev/captures"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		return s.getJSON(ctx, path)
	case "get_capture":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(args, &a)
		if a.ID == "" {
			return nil, fmt.Errorf("missing id")
		}
		return s.getJSON(ctx, "/_dev/captures/"+url.PathEscape(a.ID))
	case "clear_captures":
		return s.deleteJSON(ctx, "/_dev/captures")
	case "trigger_webhook":
		var a struct {
			EventType  string          `json:"event_type"`
			TargetURL  string          `json:"target_url"`
			DataObject json.RawMessage `json:"data_object"`
		}
		_ = json.Unmarshal(args, &a)
		if a.EventType == "" || a.TargetURL == "" {
			return nil, fmt.Errorf("event_type and target_url required")
		}
		body, _ := json.Marshal(map[string]any{
			"eventType":  a.EventType,
			"targetUrl":  a.TargetURL,
			"dataObject": a.DataObject,
		})
		return s.postJSON(ctx, "/_dev/webhooks/trigger", body)
	case "get_server_status":
		return s.getJSON(ctx, "/_dev/status")
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *server) getJSON(ctx context.Context, path string) (any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.upstream+path, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(body))
	}
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *server) postJSON(ctx context.Context, path string, body []byte) (any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.upstream+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respBody))
	}
	var v any
	_ = json.Unmarshal(respBody, &v)
	return v, nil
}

func (s *server) deleteJSON(ctx context.Context, path string) (any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, s.upstream+path, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(body))
	}
	return map[string]any{"ok": true}, nil
}
