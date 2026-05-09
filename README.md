# stripe-dev-server

A development reverse proxy for Stripe. It sits in front of Stripe's
official [`stripe-mock`](https://github.com/stripe/stripe-mock) server,
captures every request/response pair, exposes a dark-mode inspection UI,
ships a synthetic-webhook trigger, and includes an MCP server so an LLM
client (e.g. Claude Code) can query captured traffic.

Inspired by [`smtp-dev-server`](https://github.com/wrxck/smtp-dev-server)
by the same author. Single Go binary, with `stripe-mock` as the only
runtime dependency (auto-discovered or installable with one command).

> **Why?** When building apps that integrate Stripe (payments, customers,
> subscriptions, webhooks), you want CI / staging tests that exercise the
> full code path — including signed webhooks — without touching the real
> Stripe API. `stripe-mock` covers the request side; this server adds
> capture, inspection, webhook synthesis, and an MCP surface on top.

## Install

```bash
# stripe-mock (required)
go install github.com/stripe/stripe-mock@latest

# this server
go install github.com/wrxck/stripe-dev-server/cmd/stripe-dev-server@latest
```

## Quick start

```bash
stripe-dev-server
```

```
stripe-dev-server dev
A development reverse proxy for Stripe (powered by stripe-mock).
Proxy: http://127.0.0.1:12112   ·   UI/inspect: http://127.0.0.1:12113
```

Point your application at `http://127.0.0.1:12112` (instead of
`https://api.stripe.com`). Open http://127.0.0.1:12113 in your browser
to inspect captured calls and trigger fake webhooks.

```bash
# Example: create a payment intent against the proxy
curl -X POST http://127.0.0.1:12112/v1/payment_intents \
     -H 'Authorization: Bearer sk_test_anything' \
     -d 'amount=1000&currency=usd'

# It's captured, inspect it:
curl http://127.0.0.1:12113/_dev/captures
```

## Configuration

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--proxy` | `PROXY_ADDR` | `127.0.0.1:12112` | Address that forwards Stripe API calls to stripe-mock |
| `--ui` | `UI_ADDR` | `127.0.0.1:12113` | Address that serves the inspection UI + `/_dev/*` API |
| `--stripe-mock` | `STRIPE_MOCK_ADDR` | `127.0.0.1:12111` | Address of the spawned/connected stripe-mock |
| `--stripe-mock-bin` | `STRIPE_MOCK_BIN` | (auto-discover) | Path to the stripe-mock binary |
| `--no-spawn` | — | `false` | Don't spawn stripe-mock; assume it's already running at `--stripe-mock` |
| `--webhook-secret` | `WEBHOOK_SECRET` | `whsec_dev_local_secret` | Secret used to sign synthetic webhook events |
| `--max-items` | — | `1000` | Max captures retained in memory |

## Inspection / management API

| Endpoint | Behaviour |
|----------|-----------|
| `GET /_dev/captures?path=...&limit=...` | List captures (newest first, optional substring + cap) |
| `GET /_dev/captures/{id}` | Single full capture |
| `DELETE /_dev/captures` | Clear all captures |
| `POST /_dev/webhooks/trigger` | Fire a signed Stripe webhook at a target URL |
| `GET /_dev/status` | Counts + ports + stripe-mock subprocess status |

### Trigger a webhook

```bash
curl -X POST http://127.0.0.1:12113/_dev/webhooks/trigger \
  -H 'content-type: application/json' \
  -d '{
    "eventType": "payment_intent.succeeded",
    "targetUrl": "http://localhost:3000/api/webhooks/stripe",
    "dataObject": {"id": "pi_test_123", "amount": 1000}
  }'
```

The receiver gets a properly signed `Stripe-Signature` header you can
verify with the Stripe SDK using the secret you configured via
`--webhook-secret` (default `whsec_dev_local_secret`).

## MCP server

`stripe-dev-server` includes an MCP (Model Context Protocol) server so an
LLM client can query captures and trigger webhooks.

Run alongside the default mode:

```bash
# Terminal 1: the proxy + UI
stripe-dev-server

# Terminal 2: the MCP stdio server (or invoke from your MCP client)
stripe-dev-server mcp --upstream http://127.0.0.1:12113
```

For Claude Code, add to `~/.claude/mcp.json`:

```json
{
  "mcpServers": {
    "stripe-dev": {
      "command": "stripe-dev-server",
      "args": ["mcp", "--upstream", "http://127.0.0.1:12113"]
    }
  }
}
```

### Tools exposed

- `list_captures(filter_path?, limit?)` — list captured Stripe API calls
- `get_capture(id)` — full capture
- `clear_captures` — drop all captures
- `trigger_webhook(event_type, target_url, data_object?)` — fire a signed Stripe-shaped webhook
- `get_server_status` — counts + ports + subprocess status

## License

MIT — see [LICENSE.md](./LICENSE.md).
