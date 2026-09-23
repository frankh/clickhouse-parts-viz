#!/usr/bin/env bash
# Insert small batches over the last 3 days of timestamps. Each insert makes
# one new part per day it touches. Now and then send a large batch.
set -euo pipefail
ch() { clickhouse-client --host "$CLICKHOUSE_HOST" --password "$CLICKHOUSE_PASSWORD" "$@"; }

i=0
while true; do
  i=$((i + 1))
  rows=$(( (RANDOM % 20 + 1) * 500 ))
  if (( i % 50 == 0 )); then rows=500000; fi
  days=$(( RANDOM % 3 + 1 ))
  ch -q "
    INSERT INTO demo.events
    SELECT
        rand() % 100 AS team_id,
        now() - toIntervalSecond(rand() % (86400 * ${days})) AS ts,
        ['pageview', 'click', 'identify', 'custom'][rand() % 4 + 1] AS event,
        randNormal(100, 30) AS value,
        randomPrintableASCII(rand() % 200) AS payload
    FROM numbers(${rows})"
  sleep "$(awk "BEGIN { print 0.5 + ($RANDOM % 150) / 100 }")"
done
