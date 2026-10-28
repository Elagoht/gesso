# gesso guide

gesso is a Go framework for JSON APIs. This guide walks through it in the order
an application meets it; [DESIGN.md](../DESIGN.md) is the specification behind
it, with the reasons.

1. [Getting started](getting-started.md) — `gesso new`, a first module, running it
2. [Modules and handlers](modules-and-handlers.md) — routes, typed handlers,
   input, uploads, errors, endpoints
3. [Validation](validation.md) — the rules, `dive`, your own checks
4. [The database](database.md) — `db.DB`, transactions, migrations, dialects,
   `gesso gen`, dates
5. [Identity and limits](identity-and-limits.md) — bearer tokens, guards,
   rate limits, CORS
6. [Work outside the request](background.md) — the queue, events, locks
7. [Testing](testing.md) — `gessotest`, fakes and suites
8. [With collage](collage.md) — one process or two
9. [Running it](operations.md) — request ids, the access log, health, stopping
