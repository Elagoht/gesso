# Running it

## Request ids and the access log

Every answer carries `X-Request-ID`; a failure logged at Error carries the same
`request_id`, so a user's report finds its log line. `gesso.RequestIDFrom(ctx)`
gives it to a handler, to log or to pass on. Behind a proxy that sets its own
`X-Request-ID`, `Config.TrustRequestID` keeps the proxy's.

`Config.AccessLog` logs every request at Info: method, path, status, bytes,
duration, request id.

## Health

```go
gesso.New(gesso.Config{Health: &gesso.Health{
	Checks: map[string]func(context.Context) error{"database": pool.PingContext},
}}, …)
```

`/healthz` answers 200 while the process runs; `/readyz` runs the checks at once,
each bounded by `Timeout` (2s), and answers 503 when one fails — naming it,
never its error — or when the app is stopping. Both are under `BasePath`.
`gesso new` sets them up with a database check.

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
