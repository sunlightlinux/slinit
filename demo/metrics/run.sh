#!/bin/sh
# Build slinit, build the image, bring up slinit + Prometheus.
set -e
cd "$(dirname "$0")"

mkdir -p bin
for c in slinit slinitctl; do
    CGO_ENABLED=0 go build -trimpath -o "bin/$c" "../../cmd/$c"
done

docker compose up -d --build

printf '\nPrometheus:   http://localhost:9090\n'
printf 'Raw metrics:  http://localhost:9100/metrics\n'
printf '\nStop with: docker compose down\n'
