CREATE DATABASE IF NOT EXISTS demo;

CREATE TABLE IF NOT EXISTS demo.events
(
    team_id UInt32,
    ts DateTime,
    event LowCardinality(String),
    value Float64,
    payload String,
    INDEX idx_value value TYPE minmax GRANULARITY 1
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (team_id, ts)
SETTINGS old_parts_lifetime = 60;
