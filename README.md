# portfolio-backend

The animated terminal at the top of my GitHub profile is not a GIF. It is a Go
service that renders an SVG per request, with numbers fetched live.

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://pedrotessaro.fly.dev/terminal.svg?theme=light">
  <img alt="Animated terminal rendering my profile on demand" src="https://pedrotessaro.fly.dev/terminal.svg">
</picture>

```console
$ curl -s https://pedrotessaro.fly.dev/whoami
```

The command the terminal types actually works — it is the same service answering
in JSON.

---

## Why this is less trivial than it looks

GitHub does not serve a README image directly. It proxies it through **Camo**,
inside an `<img>` tag. That imposes three constraints which shape the whole
project:

| Constraint | Consequence |
|---|---|
| `<img>` does not run JavaScript | The animation has to be **SMIL**, not JS |
| External resources do not load | No webfont; only the system monospace stack |
| Camo caches aggressively | Without the right headers the image freezes and the numbers stop moving |

### The typing effect

Each line is revealed by a `<clipPath>` whose rectangle grows one character at a
time. The detail that separates typing from a curtain sliding open is
`calcMode="discrete"`:

```xml
<clipPath id="type0">
  <rect x="26" y="47" width="0" height="25">
    <animate attributeName="width" values="0;9;18;27;…"
             calcMode="discrete" dur="2.2s" begin="0.35s" fill="freeze"/>
  </rect>
</clipPath>
```

### Alignment cannot depend on the visitor's fonts

If the visitor does not have the expected monospace, the browser substitutes one
with different metrics, and a clip computed in 9px steps lands mid-character.

Declaring `textLength` on every run of text pins the geometry to what the server
computed instead of whatever the local font happens to produce:

```go
`<text x="%s" y="%s" fill="%s" textLength="%s" lengthAdjust="spacingAndGlyphs" …>`
```

### The latency on screen is honest

The SVG reports how long it took to build itself. For that number to mean
anything, no request can wait on the GitHub API: a goroutine refreshes the stats
every 15 minutes and the handlers only read the cache. If GitHub goes down, the
last good values stay up marked `cached` — a README with 20-minute-old numbers
beats a broken one.

---

## Architecture

```
                    ┌──────────────────────────────┐
   GitHub README ──▶│  Camo (image proxy)          │
                    └──────────────┬───────────────┘
                                   │  GET /terminal.svg
                    ┌──────────────▼───────────────┐
                    │  httpapi   handlers, headers │
                    └──┬────────┬─────────┬────────┘
                       │        │         │
              ┌────────▼──┐ ┌───▼─────┐ ┌─▼──────────┐
              │ svgterm   │ │ store   │ │ githubapi  │
              │ draws the │ │ SQLite  │ │ cache +    │
              │ SVG       │ │ (volume)│ │ bg refresh │
              └───────────┘ └─────────┘ └─────┬──────┘
                                              │ every 15 min
                                        ┌─────▼──────┐
                                        │ api.github │
                                        └────────────┘
```

| Package | Responsibility |
|---|---|
| `internal/svgterm` | Builds the SVG: geometry, palette, animation timeline |
| `internal/githubapi` | Client with background refresh and stale fallback |
| `internal/store` | View counter in SQLite, rolled up per day |
| `internal/config` | Editorial content in YAML, validated at boot |
| `internal/httpapi` | Routes, anti-cache headers, structured logging |

## Endpoints

| Route | What it does |
|---|---|
| `GET /terminal.svg` | The animated terminal. `?theme=light`, `?static=1` |
| `GET /whoami` | The JSON the terminal claims to fetch |
| `GET /healthz` | Health check consumed by Fly |
| `GET /metrics` | Prometheus exposition format |

`?static=1` draws the final frame without SMIL, for renderers that ignore
animation — GitHub's social card and some feed readers would otherwise show an
empty window.

## Running locally

```bash
make run
open http://localhost:8080/terminal.svg
```

Without `GITHUB_TOKEN` the service works, but the commits column disappears:
contributions only exist on the GraphQL API, which requires auth.

```bash
GITHUB_TOKEN=ghp_… make run
```

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `CONFIG_PATH` | `config.yaml` | Terminal content |
| `DB_PATH` | `data/portfolio.db` | SQLite file for the counter |
| `GITHUB_TOKEN` | — | Enables the contributions count |
| `FLY_REGION` | `local` | Set by Fly in production |

Changing the bio or the stack means editing `config.yaml` — no rebuild.

## Tests

```bash
make test
make race
make cover
```

The central test checks that the SVG is **well-formed XML**: browsers drop an
invalid document silently, with nothing in the server logs — the README would
just show a broken image.

## Deploy

```bash
fly launch --no-deploy
fly volumes create portfolio_data --size 1 --region gru
make deploy
```

## Decisions worth explaining

**SQLite rolled up per day, not one row per visit.** The total stays exact,
"today" is a single-row lookup, and the table grows ~365 rows a year.

**Pure-Go SQLite driver** (`modernc.org/sqlite`). No cgo means a static binary on
a ~2 MB distroless image, with no libc.

**A single connection in the pool.** SQLite serialises writes anyway; more
connections would only buy contention and `SQLITE_BUSY`.

**Config validated at boot.** An empty field kills the process immediately
instead of becoming a hole in the SVG served to visitors.
