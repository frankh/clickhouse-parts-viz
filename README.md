# clickhouse-parts-viz

A small web service that shows the parts of a ClickHouse MergeTree table.

- Each partition is one row. The parts are in block-number order, left to right.
- The width of a part shows its size on disk. You can change the scale (sqrt, log, linear), or use the block range or the row count.
- The block numbers are along the bottom of each row.
- The colour of a part shows its merge level.
- Brackets above the parts show:
  - merges that run now (`system.merges`), with the progress as a fill,
  - queued merges for Replicated tables (`system.replication_queue`),
  - candidate merges: the merges that ClickHouse will probably select next.
- When you hover on a part, you see its size, rows, level, timestamps (from `system.parts` and `system.part_log`), and the minmax values of its partition-key columns and minmax skipping-index columns.
- A table view gives the same data as text.

The service is one Go binary with no external dependencies. It talks to ClickHouse through the HTTP interface. The frontend is plain JS and SVG, embedded in the binary.

## Quick start (demo)

```sh
docker compose -f deploy/docker-compose.yml up --build -d
open http://localhost:8080
```

This starts ClickHouse, a seeder that inserts small batches all the time, and the service. Set `PARTSVIZ_PORT` to use a different host port.

To see many candidates, stop merges for a short time:

```sh
docker compose -f deploy/docker-compose.yml exec clickhouse \
  clickhouse-client --password demo -q "SYSTEM STOP MERGES demo.events"
```

Use `SYSTEM START MERGES demo.events` to start them again.

## Run against your ClickHouse

```sh
CLICKHOUSE_URL=https://my-clickhouse:8443 \
CLICKHOUSE_USER=partsviz \
CLICKHOUSE_PASSWORD=... \
go run ./cmd/partsviz
```

| Variable | Default | Description |
|---|---|---|
| `CLICKHOUSE_URL` | `http://localhost:8123` | ClickHouse HTTP(S) endpoint. |
| `CLICKHOUSE_USER` | | User name. |
| `CLICKHOUSE_PASSWORD` | | Password. |
| `CLICKHOUSE_CLUSTER` | | If set, read system tables from all replicas with `clusterAllReplicas` and show a host selector. Parts are per replica, so the page shows one host at a time. |
| `CLICKHOUSE_MAX_EXECUTION_TIME` | | If set, send `max_execution_time` with each query. The user must have `readonly=0` or `readonly=2`. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |

### Permissions

Use a read-only user. The service needs `SELECT` on:

- `system.tables`, `system.columns`, `system.data_skipping_indices`, `system.parts`, `system.merges`, `system.replication_queue`, `system.part_log`, `system.merge_tree_settings`,
- the tables that you want to see, for the minmax values.

The minmax values come from a query on the table data (`min`/`max` per `_part`, for one partition). The page sends this query only when you hover on a part. The server keeps the result for 30 seconds.

The service has no authentication. Do not expose it to the internet. Put it behind your VPN or an auth proxy.

## How the candidate merges are calculated

`internal/selector` is a Go port of ClickHouse's `SimpleMergeSelector`
(`src/Storages/MergeTree/Compaction/MergeSelectors/SimpleMergeSelector.cpp`):

1. The active parts of each partition are sorted by block number.
2. Parts that are in a running or queued merge break the partition into separate ranges.
3. Each sub-range gets a score. The same `allow()` rules (base, age, size, part count) and heuristics as ClickHouse apply.
4. The best ranges that do not overlap are the candidates. Candidate #1 is the next pick.

The settings come from `system.merge_tree_settings` and the table's own `SETTINGS` clause.

The result is an estimate. ClickHouse also uses the number of free slots in the merge pool and the free disk space. This tool does not see these. The random parts of the `StochasticSimple` selector are not included.

## Development

```sh
make test   # go vet + go test
make run    # run locally
make demo   # docker compose demo
```
