# portfolio-backend

Go service behind the terminal at the top of my GitHub profile. Builds an SVG per
request: the project listing, the counts and its own response time are all read
when you load the page.

<p>
  <picture>
    <source media="(prefers-color-scheme: light)" srcset="https://pedrotessaro.vercel.app/terminal.svg?theme=light">
    <img alt="Animated terminal rendering my profile on demand" src="https://pedrotessaro.vercel.app/terminal.svg">
  </picture>
</p>

The command it types works:

```console
$ curl -s https://pedrotessaro.vercel.app/whoami
```

## Animating a README

GitHub proxies README images through Camo and drops them inside an `<img>`.
Nothing runs in there: no JavaScript, no webfonts, no fetches. Camo also caches
hard enough that without `no-store` the image freezes on whatever the first
visitor happened to see.

So the animation is SMIL. Each line sits behind a `<clipPath>` whose rect widens
one character at a time:

```xml
<rect x="26" y="47" width="0" height="25">
  <animate attributeName="width" values="0;9;18;27;…"
           calcMode="discrete" dur="2.2s" begin="0.35s" fill="freeze"/>
</rect>
```

`calcMode="discrete"` matters. Interpolated, it's a curtain sliding open rather
than someone typing.

The subtler problem took longer to find. I tested on a machine that didn't have
my monospace installed, the browser substituted a font with different metrics,
and the clip — stepping in fixed 9px increments — started cutting glyphs in half.
Every run of text now declares `textLength`, which pins it to the width the
server computed instead of whatever the local font produces. Runs are separate
`<text>` elements rather than `<tspan>`s so each one starts at a column that's
already known.

## Where the numbers come from

`internal/config/profile.yaml` holds the identity and a list of repo names,
nothing else. Language, stars and last push come from the API, ordered by push
date. Rename a repo and it drops out of the listing with a warning in the logs
rather than breaking the render.

The `ci passing · tests 59 · coverage 82%` line is written by the workflow, not
by me. It runs gofmt, vet and the race detector, then posts the results to
`/internal/ci`. It holds a token for that one endpoint rather than credentials
for the database.

Each source degrades on its own: no GitHub token means no commits column, no
Redis means no view counter, nothing configured at all still renders a terminal.

## Serverless

This ran on Fly first, as an ordinary server with SQLite on a volume. Vercel has
no disk and no process between requests, so:

- the counter moved to Redis over its HTTP API, no driver and no pool to keep warm
- the background refresh goroutine became a two-layer cache — in the instance
  while it stays warm, in Redis so a cold start inherits what an earlier one
  fetched
- uptime became cold/warm, there being no process to have an uptime

Everything the render needs from Redis travels in one pipelined round trip, since
that round trip is on the request path and lands in the number the SVG prints.

The latency sample written on each request is the duration of the *previous* one
this instance served. Writing after the response is flushed is a bet on a frozen
instance waking up to finish the job.

The stats cache key carries the build. I learned that one the hard way: added a
field, deployed, and production kept serving the previous shape for a full TTL
because it still decoded cleanly with the new field empty.

## Endpoints

| | |
|---|---|
| `GET /terminal.svg` | the terminal. `?theme=light`, `?static=1` |
| `GET /whoami` | the JSON the terminal claims to fetch |
| `GET /healthz` | |
| `GET /metrics` | Prometheus exposition format |
| `POST /internal/ci` | bearer token, used by the workflow |

`?static=1` skips SMIL and draws the final frame, for renderers that ignore
animation and would otherwise show an empty window.

## Running it

```bash
make run
make test
make race
```

Works unconfigured, with fewer numbers. `GITHUB_TOKEN` enables the commits
column. `KV_REST_API_URL` and `KV_REST_API_TOKEN` enable the counter, the latency
window and the CI line. `CI_PUBLISH_TOKEN` has to match the repository secret of
the same name or the publish endpoint stays closed. `CONFIG_PATH` overrides the
embedded YAML.

The test worth having parses the output as XML. A malformed SVG is dropped
silently by the browser, so the failure mode is a broken image in the README and
nothing at all in the logs.

## Known issues

- A cold start that also misses the Redis cache calls GitHub inline and costs a
  few hundred ms. Warm requests are around 1ms.
- The percentile line hides below 30 samples, which also means it disappears
  after a quiet week — the window is 500 entries and never expires.
- `?static=1` isn't cached and should be.

## Self-hosting

The Dockerfile builds the same binary as a plain HTTP server. cgo off, so it's
static: 6.9 MB on distroless.
