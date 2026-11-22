#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
lab=$(mktemp -d)
trap 'rm -rf -- "$lab"' EXIT
go test -c -o "$lab/backup.test" ./internal/backup
cp tests/resilience/connectors.sh "$lab/"
docker build -t backuphub-resilience-fixtures tests/resilience
docker run --rm --network none --tmpfs /bh-small-volume:size=48m,mode=0700 \
  -v "$lab:/lab:ro" backuphub-resilience-fixtures bash /lab/connectors.sh
