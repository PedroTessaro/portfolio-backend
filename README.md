# portfolio-backend

The terminal at the top of my GitHub profile is this service. Every time someone
opens my profile it renders a fresh SVG: the project listing, the counts and the
response time are all read at request time. My profile README is the image and a
row of links, nothing else.

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://pedrotessaro.vercel.app/terminal.svg?theme=light">
  <img alt="Animated terminal rendering my profile on demand" src="https://pedrotessaro.vercel.app/terminal.svg">
</picture>

The command it types is real:

```console
$ curl -s https://pedrotessaro.vercel.app/whoami
```

## Why SMIL

GitHub doesn't load README images from your server. They go through Camo, its
image proxy, and land inside an `<img>` tag. Nothing runs in there: no
JavaScript, no webfonts, no fetches. And Camo caches hard, so without
`no-store` the image freezes and the numbers stay stuck at whatever they were
the first time someone looked.

That leaves SMIL. Each line sits behind a `<clipPath>` whose rectangle widens one
character at a time:

```xml
<rect x="26" y="47" width="0" height="25">
  <animate attributeName="width" values="0;9;18;27;…"
           calcMode="discrete" dur="2.2s" begin="0.35s" fill="freeze"/>
</rect>
```

`calcMode="discrete"` is the whole trick. Without it the width interpolates
smoothly and you get a curtain sliding open instead of someone typing.

## The font problem

I had this working and then tried it on a machine without my monospace installed.
The browser substituted something with different metrics, and the clip — stepping
in fixed 9px increments — started cutting glyphs in half.

Fix is `textLength` on every run of text:

```go
`<text x="%s" y="%s" fill="%s" textLength="%s" lengthAdjust="spacingAndGlyphs" …>`
```

That forces each run into the width I computed regardless of what font actually
renders. Segments are separate `<text>` elements rather than `<tspan>`s for the
same reason: every one starts at a column I already know.

## Serverless changed the design

This ran on Fly first, as a normal server with SQLite on a volume. Vercel has no
disk and no process between requests, so three things had to change.

The view counter moved to Redis over its HTTP API — no driver, no pool to keep
warm. It's optional: with no credentials configured the counter line just
disappears from the SVG instead of rendering zeros.

The GitHub numbers were refreshed by a background goroutine, which doesn't exist
here. Now they're cached in the instance while it's warm, and in Redis so a cold
start inherits whatever an earlier invocation fetched. Only the request that
misses both pays for the API call.

And there's no uptime to report, so that line says whether the instance was cold
or warm. Less impressive, but true.

## Nothing is written twice

The featured projects are named in `internal/config/profile.yaml` — just the
names:

```yaml
projects:
  - "portfolio-backend"
  - "RSSAggregator"
  - "AssemblerImplementation"
```

Language, star count and last push come from the API, and the listing is ordered
by push date so `ls -lt` isn't a lie. A name that no longer resolves gets skipped
with a warning rather than breaking the render, which is what happens when you
rename a repo and forget this file exists.

The reason for curating at all: sorting purely by recency puts whatever I last
poked at on top, and that is usually a scratch repo rather than something worth
showing.

## Endpoints

- `GET /terminal.svg` — the terminal. `?theme=light` and `?static=1` both work
- `GET /whoami` — the JSON the terminal claims to fetch
- `GET /healthz`
- `GET /metrics` — Prometheus exposition format

`?static=1` skips SMIL and draws the final frame. GitHub's social card and some
feed readers ignore animation, and would otherwise show an empty window.

## Running it

```bash
make run
open http://localhost:8080/terminal.svg
```

Works with no configuration at all, just with fewer numbers. `GITHUB_TOKEN`
enables the commits column (contributions are GraphQL-only, which needs auth).
`KV_REST_API_URL` and `KV_REST_API_TOKEN` enable the view counter — Vercel's
Upstash integration sets both. `CONFIG_PATH` overrides the embedded YAML, which
is handy when you're iterating on the text.

The content itself lives in `internal/config/profile.yaml`. It's embedded with
`go:embed` because the function ships as a binary with no repo around it.

```bash
make test
make race
```

The test I actually care about parses the output as XML. An invalid SVG gets
dropped silently by the browser — nothing in the logs, just a broken image in the
README — so it's the failure most worth catching.

## Rough edges

Cold starts cost about 1.3s when the Redis cache has also expired, because that
request goes out to GitHub synchronously. Warm requests are under a millisecond.
I'd rather have that than a background job I can't run here, but it does mean the
occasional visitor waits.

`?static=1` output isn't cached either, and it probably should be.

## Self-hosting

There's a Dockerfile. Same handler, running as a plain HTTP server instead of a
function. cgo is off so the binary is static (6.9 MB) and the base image is
distroless.
