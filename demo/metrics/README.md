# slinit metrics in a browser

slinit as PID 1 in a container, serving Prometheus metrics, with a
Prometheus scraping it. Two commands and you have graphs.

```bash
cd demo/metrics
./run.sh
```

Then open **<http://localhost:9090>** — that is Prometheus. The raw
exposition is at **<http://localhost:9100/metrics>** if you would
rather read text.

From another machine, replace `localhost` with the host's address; both
ports are published on all interfaces.

To stop: `docker compose down`.

## Queries worth typing first

Paste these into Prometheus's expression box and press Execute, then
switch to the Graph tab.

| Query | What you see |
|-------|--------------|
| `slinit_services` | Services by state. One is `failed` on purpose. |
| `slinit_service_up` | Per-service up/down. `doomed` is 0. |
| `rate(slinit_service_restarts_total[1m]) * 60` | Restarts per minute. `flapping` sits around 10; everything else is flat at 0. |
| `slinit_service_startup_seconds` | How long each service took to start. `slow-starter` is ~4s, the rest are sub-millisecond. |
| `slinit_boot_userspace_seconds` | How long the whole boot took. |

The restart rate is the one to put on a graph — it is the only metric
here that moves while you watch.

## The services

Five, chosen so the numbers are not all zeroes:

| Service | Does |
|---------|------|
| `boot` | The target everything else hangs off. |
| `steady` | Runs forever. `slinit_service_up` stays 1. |
| `flapping` | Exits every 5 seconds with `restart = yes`, so the restart counter climbs. |
| `slow-starter` | Takes 4 seconds to report started, so startup times are not uniform. |
| `doomed` | Start command exits non-zero, so it lands in `failed` and stays. |

Edit anything under `services/` and re-run `./run.sh` — it rebuilds.

## Two things the numbers will tell you that are not true

**`slinit_boot_kernel_seconds` is meaningless here.** In container mode
there is no kernel of slinit's own to have booted; the figure comes
from `/proc/uptime`, which inside a container is the *host's*. On a
real PID 1 it means what it says.

**`slinit_build_info{version="dev"}`** — the demo builds without the
`-ldflags -X main.version=` that a release build passes.

## What is actually being demonstrated

The endpoint is served by hand-written HTTP in `pkg/metrics`, not by
`net/http`. That is deliberate: this code runs in PID 1, and linking
the standard HTTP stack into the init process pulls in a large amount
of surface for a page that renders eleven metrics. The whole server is
a listener, a read deadline and a `Sprintf`.

It is off unless you ask for it — `--metrics-listen` takes a
`host:port` or `unix:/path` and is empty by default. See
`slinit(8)`, and STABILITY.md for what the metric names promise
(names and types are fixed within a major version; `_total` counters
stay monotonic).
