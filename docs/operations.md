# Running it

## Request ids and the access log

Every answer carries `X-Request-ID`; a failure logged at Error carries the same
`request_id`, so a user's report finds its log line. `gesso.RequestIDFrom(ctx)`
gives it to a handler, to log or to pass on. Behind a proxy that sets its own
`X-Request-ID`, `Config.TrustRequestID` keeps the proxy's.

`Config.AccessLog` logs every request at Info: method, path, status, bytes,
duration, request id.

## Metrics and traces

```go
gesso.New(gesso.Config{Metrics: &gesso.Metrics{Token: os.Getenv("METRICS_TOKEN")}})
```

`/metrics` serves Prometheus: requests by method, route pattern and status,
their latency, those in flight. A `Config.Tracer` begins a span per request —
an OpenTelemetry adapter is a few lines:

```go
type otelTracer struct{ t trace.Tracer }

func (o otelTracer) Start(r *http.Request, route string) (context.Context, func(int)) {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := o.t.Start(ctx, route)
	return ctx, func(status int) {
		span.SetAttributes(attribute.Int("http.status_code", status))
		span.End()
	}
}
```

## Health

```go
gesso.New(gesso.Config{Database: sqlite.Open("notes.db"), Health: &gesso.Health{}})
```

`/healthz` answers 200 while the process runs; `/readyz` runs the checks at once,
each bounded by `Timeout` (2s), and answers 503 when one fails — naming it,
never its error — or when the app is stopping. Both are under `BasePath`.
With a `Config.Database`, the app adds a `database` check of its own;
`Health.Checks` adds others, by name.

## Timeouts

`Run` gives a request a minute to arrive (`ReadTimeout`, its body included) and
a minute to be answered (`WriteTimeout`), and closes a kept-alive connection
idle for two (`IdleTimeout`). Raise `ReadTimeout` for large uploads over slow
links.

## Stopping

`Run` stops on SIGINT or SIGTERM: readiness turns 503, `DrainDelay` passes —
time for a load balancer to stop sending, the server still serving — then the
server stops taking requests and waits for those in flight (`ShutdownTimeout`),
then the modules stop, then the `OnShutdown` hooks run (the database closes).
