# es-querycost

[![build](https://github.com/matthewgall/es-querycost/actions/workflows/build.yml/badge.svg)](https://github.com/matthewgall/es-querycost/actions/workflows/build.yml)

A small Go proxy that sits in front of an Elasticsearch cluster, estimates the
cost of a Lucene query, applies plan-based rules, and either rejects the request
or forwards the query to Elasticsearch.

## What it does

Customers aren't experts, they send complicated queries without even knowing:

```
asn:AS13335 AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl*)
```

Leading wildcards on `.keyword` fields are expensive. They can cause a cluster to
slow down, refuse requests or not respond. `es-querycost` parses the
query string locally, scores it, applies policy rules, and either returns
`402 Payment Required` with a cost report or proxies the original request to
an upstream Elasticsearch unchanged.

## Features

- **AST-based cost estimation** for Lucene query strings.
- **Plan-based rules**: cost limits, date-window requirements, and forbidden
  features such as `match_all` and open ranges.
- **Request-source passthrough**: a gated `source` object lets callers pass
  `size`, `from`, `sort`, `_source`, `fields`, `track_total_hits`, and `collapse`.
- **Authentication**: none, API key, HMAC JWT, or JWKS (HTTPS-only, algorithm
  allowlist).
- **TLS options** for upstream Elasticsearch, including custom CA and
  `insecure_skip_verify` for testing.
- **Trusted proxy CIDRs** for safe `X-Forwarded-For` resolution.
- **Prometheus metrics** endpoint protected by the same authenticator.
- **Hot reload** of plans, the cost model, validator, and authenticator.
- **Atomic config swapping** to avoid races during reload.

## Repository layout

```
cmd/es-querycost          // HTTP server entry point
internal/config           // Viper-based config (file, env, flags, env substitution)
internal/query            // Lucene query parser and AST
internal/cost             // Query cost estimation
internal/datemath         // Elasticsearch date-math parser
internal/rules            // Policy rule engine
internal/auth             // Authentication (API key, JWT, JWKS)
internal/metrics          // Prometheus metrics
internal/proxy            // HTTP gate and Elasticsearch forwarding
internal/validate         // Optional Elasticsearch pre-validation
```

## How it works

1. Receives `POST /search` with a top-level `query`, optional `index`, optional
   `source`, and a `context` object (e.g. `{ "user": { "plan": "free" } }`).
2. Authenticates the request. The auth-derived plan and user override any
   plan provided in the JSON body.
3. Parses the query into an AST and estimates its cost.
4. Runs it through the rule engine:
   - `ForbiddenFeature` rejects `match_all` and open ranges for everyone.
   - `PlanLimit` caps total cost per plan.
   - `QueryWindow` caps the queryable time window on configured date fields.
5. Optionally validates the query against Elasticsearch (`query.validator:
   elasticsearch`). If the local parser fails but Elasticsearch accepts the
   query, the gate parses Elasticsearch's normalised explanation and continues
   estimating cost.
6. If allowed, it builds the upstream `_search` URL, injects a date window if
   one is required, and proxies the request body to Elasticsearch.
7. Returns the upstream status code, headers, and body unchanged.

## Quick start

Copy `config.minimal.yaml` and run:

```bash
go run ./cmd/es-querycost --config config.minimal.yaml
```

Send a request:

```bash
curl -X POST http://localhost:8080/search \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "asn:AS13335 AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl*)",
    "index": "my-index",
    "context": { "user": { "plan": "free" } }
  }'
```

A rejected request returns `402 Payment Required`:

```json
{
  "allowed": false,
  "cost": 551,
  "reason": "query cost 551.00 exceeds free plan limit of 50.00",
  "report": {
    "Cost": 551,
    "Breakdown": { ... },
    "Explanation": [ ... ]
  }
}
```

## Endpoints

| Method | Path        | Description                                                                     |
| ------ | ----------- | ------------------------------------------------------------------------------- |
| `POST` | `/search`   | Estimate cost, apply rules, and proxy allowed queries to Elasticsearch.         |
| `POST` | `/validate` | Estimate cost and apply rules without proxying to Elasticsearch.                |
| `GET`  | `/healthz`  | Health check; returns `200 ok`.                                                 |
| `GET`  | `/metrics`  | Prometheus metrics (requires authentication when `auth` is enabled).            |

Only `POST` is allowed on `/search` and `/validate`.

## Configuration

Configuration is loaded in order of increasing precedence:

1. Built-in defaults
2. Config file (`es-querycost.yaml`, `es-querycost.json`, `es-querycost.toml`)
3. Environment variables with the `ESQUERY_` prefix
4. Command-line flags

Config files support environment variable substitution (`$VAR` and `${VAR}`).

The config is grouped into nested blocks:

```yaml
server:
  listen_addr: "127.0.0.1:8080"
  proxy_timeout: "30s"
  trusted_proxies: []

elasticsearch:
  url: "http://localhost:9200"

query:
  validator: "local"
  date_field: "@timestamp"
  date_fields:
    - "@timestamp"
    - "timestamp"
    - "date"
    - "created_at"
  require_window: false
  forbidden_features:
    - "match_all"
    - "open_range"

limits:
  cost_model:
    term_cost: 1
    phrase_cost: 2
    wildcard_multiplier: 10
    leading_wildcard_cost: 50
    match_all_cost: 100
    range_cost: 5
    open_range_cost: 50
    or_clause_cost: 3
    depth_cost: 1
    keyword_wildcard_multiplier: 3
    field_weights:
      asn: 0.5
    default_field_weight: 1
    default_search_field_penalty: 2

  plans:
    free:
      cost_limit: 50
      window: "14d"
    starter:
      cost_limit: 200
      window: "30d"
    pro:
      cost_limit: 1000
      window: "60d"
    enterprise:
      cost_limit: 10000
      window: "365d"

metrics:
  enabled: true
  path: "/metrics"

auth:
  type: "none"

logging:
  level: "info"
  format: "json"
  requests: true
```

See `config.example.yaml` for a fully annotated version and
`config.minimal.yaml` for a local-dev starting point.

### Command-line flags

```bash
go run ./cmd/es-querycost \
  --config config.minimal.yaml \
  --listen-addr :8080 \
  --elasticsearch-url http://localhost:9200
```

### Environment variables

Nested keys become underscored environment variables with the `ESQUERY_`
prefix:

```bash
ESQUERY_SERVER_LISTEN_ADDR=:8080 \
ESQUERY_ELASTICSEARCH_URL=https://es.example.com:9200 \
ESQUERY_QUERY_VALIDATOR=elasticsearch \
ESQUERY_AUTH_TYPE=apikey \
  go run ./cmd/es-querycost
```

## Authentication

Set `auth.type` to `none`, `apikey`, `jwt`, or `jwks`.

### API key

```yaml
auth:
  type: "apikey"
  api_keys:
    my-secret-key:
      user_id: "user-1"
      plan: "pro"
```

```bash
curl -X POST http://localhost:8080/search \
  -H "Authorization: ApiKey my-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"query": "asn:AS13335", "index": "my-index"}'
```

Keys are stored as SHA-256 hashes at runtime. The downstream `Authorization`
header is never forwarded to Elasticsearch.

### JWT (HMAC)

```yaml
auth:
  type: "jwt"
  jwt_secret: "change-me"
  jwt_plan_claim: "plan"
  jwt_user_claim: "sub"
```

Request header: `Authorization: Bearer <token>`.

### JWT (JWKS)

```yaml
auth:
  type: "jwks"
  jwks_url: "https://idp.example.com/.well-known/jwks.json"
  jwt_issuer: "https://idp.example.com"
  jwt_audience: "es-querycost"
  jwt_plan_claim: "plan"
  jwt_user_claim: "sub"
```

The JWKS URL must be `https`. Allowed signing algorithms are `RS256`, `RS384`,
`RS512`, `ES256`, `ES384`, `ES512`, and `EdDSA`.

## Upstream TLS

```yaml
elasticsearch:
  url: "https://localhost:9200"
  # ca_cert: "/path/to/ca.crt"
  # insecure_skip_verify: true   # testing only
  # username: "elastic"
  # password: "changeme"
```

If `insecure_skip_verify` is true, certificate verification is skipped for the
upstream connection. The downstream `Authorization` header is never forwarded,
so `username`/`password` are the safe way to authenticate to Elasticsearch.

## Client IP trust

By default the gate ignores `X-Forwarded-For` and uses the direct peer address.
To trust a reverse proxy, list its CIDRs:

```yaml
server:
  trusted_proxies:
    - "10.0.0.0/8"
    - "127.0.0.1/32"
```

The rightmost non-proxy address becomes `client_ip`. `X-Real-Ip` and other
forwarding headers are sanitised or rewritten at the proxy boundary and are
never forwarded upstream.

## Metrics

When `metrics.enabled` is true (default), Prometheus metrics are exposed at
`metrics.path`. The endpoint is protected by the configured authenticator:

| Metric | Type | Labels | Description |
| ------ | ---- | ------ | ----------- |
| `es_querycost_requests_total` | counter | `path` | Total requests received. |
| `es_querycost_denials_total` | counter | `reason` | Denied queries by reason. |
| `es_querycost_evaluation_duration_seconds` | histogram | — | Parse + cost + rule evaluation time. |
| `es_querycost_proxy_duration_seconds` | histogram | — | Time proxying to Elasticsearch. |
| `es_querycost_query_cost` | histogram | — | Observed query cost distribution. |

## Cost model

The gate estimates cost using these rules:

- Plain terms are cheap; exact terms on low-cardinality fields are cheaper.
- Wildcards, prefix queries, and leading wildcards are expensive.
- Leading wildcards on `.keyword` fields are the most expensive.
- `*:*` and open ranges `[* TO *]` are very expensive.
- `MUST` clauses use the maximum child cost (intersection is bounded).
- `SHOULD` clauses sum child costs (unions grow with each clause).
- `MUST_NOT` adds a fixed penalty.
- Queries against the implicit default field are penalised.
- Phrase cost scales with the number of terms.
- Per-field weights reflect real index cardinality.

## Date windows

Each plan defines a maximum query window. If the query already constrains one of
the configured `date_fields`, the existing window is used. Otherwise:

- when `query.require_window` is `true`, the request is rejected;
- when `query.require_window` is `false`, the plan window is silently injected
  as `+(<user query>) +date_field:[now-<window> TO now]`.

## `/validate` endpoint

`POST /validate` estimates cost and applies rules without proxying to
Elasticsearch, useful for client-side cost checks or CI.

```bash
curl -X POST http://localhost:8080/validate \
  -H 'Content-Type: application/json' \
  -d '{"query": "asn:AS13335", "index": "my-index"}'
```

Allowed queries return `200`:

```json
{"allowed": true, "cost": 0.5, "report": {...}}
```

Denied queries return `402`:

```json
{"allowed": false, "cost": 367, "reason": "...", "report": {...}}
```

## Source-field allowlist

The optional `source` field lets callers pass extra Elasticsearch query
parameters. Only these keys are allowed:

- `size`
- `from`
- `sort`
- `_source`
- `fields`
- `track_total_hits`
- `collapse`

`source.query` is not allowed; use the top-level `query` field so the gate can
estimate cost.

## Hot reload and graceful shutdown

When started with a config file, the gate reloads plans, the cost model,
validator, and authenticator automatically on change. Listen address and log
level still require a restart.

The server handles `SIGINT` and `SIGTERM` and shuts down gracefully, closing JWKS
and other background authenticator resources.

## Security notes

- Authentication context from credentials always overrides the request-body
  context, so callers cannot escalate plans.
- The downstream `Authorization` header is stripped before forwarding to
  Elasticsearch; use `elasticsearch.username`/`password` for upstream basic auth.
- Forwarding headers are sanitised at the proxy boundary.
- API keys are hashed with SHA-256 at runtime.
- JWT/JWKS validation enforces an algorithm allowlist and supports issuer and
  audience checks.

## Default plan limits

| Plan       | Max cost | Max query window |
| ---------- | -------: | ---------------: |
| free       |       50 |           14 days |
| starter    |      200 |           30 days |
| pro        |     1000 |           60 days |
| enterprise |    10000 |          365 days |

Unknown plans fall back to a cost limit of `100` and a `30d` window.

## Local integration testing

A `docker-compose.yml` is included for local Elasticsearch.

```bash
docker compose up -d
```

Then run the integration tests:

```bash
ESQUERY_TEST_ELASTICSEARCH_URL=http://localhost:9200 \
  go test -tags integration ./internal/integration -v
```

If Elasticsearch is not running, the live integration tests are skipped.

Run the gate locally:

```bash
ESQUERY_ELASTICSEARCH_URL=http://localhost:9200 go run ./cmd/es-querycost
```

## Unit tests

```bash
go test ./...
```

With the race detector:

```bash
go test -race ./...
```

## License
[MIT](LICENSE)