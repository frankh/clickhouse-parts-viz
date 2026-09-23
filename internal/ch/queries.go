package ch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Store runs the queries the service needs. If Cluster is set, system tables
// are read from every replica with clusterAllReplicas and filtered by host.
type Store struct {
	Client  *Client
	Cluster string
}

func (s *Store) src(table string) string {
	if s.Cluster == "" {
		return table
	}
	return fmt.Sprintf("clusterAllReplicas(%s, %s)", Literal(s.Cluster), table)
}

func (s *Store) hostFilter(host string, params map[string]string) string {
	if s.Cluster == "" || host == "" {
		return ""
	}
	params["host"] = host
	return " AND hostName() = {host:String}"
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := Query[struct{}](ctx, s.Client, "SELECT 1", nil)
	return err
}

func (s *Store) Hosts(ctx context.Context) ([]string, error) {
	if s.Cluster == "" {
		return nil, nil
	}
	rows, err := Query[struct {
		Host string `json:"host"`
	}](ctx, s.Client, "SELECT DISTINCT hostName() AS host FROM "+s.src("system.one")+" ORDER BY host", nil)
	if err != nil {
		return nil, err
	}
	hosts := make([]string, len(rows))
	for i, r := range rows {
		hosts[i] = r.Host
	}
	return hosts, nil
}

type TableSummary struct {
	Database string `json:"database"`
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	Parts    U64    `json:"parts"`
	Bytes    U64    `json:"bytes"`
}

func (s *Store) Tables(ctx context.Context) ([]TableSummary, error) {
	return Query[TableSummary](ctx, s.Client, `
SELECT t.database AS database, t.name AS name, t.engine AS engine, p.parts AS parts, p.bytes AS bytes
FROM system.tables AS t
LEFT JOIN (
    SELECT database, table, count() AS parts, sum(bytes_on_disk) AS bytes
    FROM system.parts WHERE active GROUP BY database, table
) AS p ON p.database = t.database AND p.table = t.name
WHERE t.engine LIKE '%MergeTree%' AND t.database NOT IN ('INFORMATION_SCHEMA', 'information_schema')
ORDER BY p.bytes DESC, t.database, t.name`, nil)
}

type TableInfo struct {
	Database      string            `json:"database"`
	Name          string            `json:"name"`
	Engine        string            `json:"engine"`
	EngineFull    string            `json:"-"`
	PartitionKey  string            `json:"partition_key"`
	SortingKey    string            `json:"sorting_key"`
	MinMaxColumns []string          `json:"minmax_columns"`
	Settings      map[string]string `json:"table_settings"`
}

// Table returns nil if the table does not exist or is not a MergeTree table.
func (s *Store) Table(ctx context.Context, database, table string) (*TableInfo, error) {
	params := map[string]string{"db": database, "table": table}
	rows, err := Query[struct {
		Engine       string `json:"engine"`
		EngineFull   string `json:"engine_full"`
		PartitionKey string `json:"partition_key"`
		SortingKey   string `json:"sorting_key"`
	}](ctx, s.Client, `
SELECT engine, engine_full, partition_key, sorting_key FROM system.tables
WHERE database = {db:String} AND name = {table:String} AND engine LIKE '%MergeTree%'`, params)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	info := &TableInfo{
		Database:     database,
		Name:         table,
		Engine:       rows[0].Engine,
		EngineFull:   rows[0].EngineFull,
		PartitionKey: rows[0].PartitionKey,
		SortingKey:   rows[0].SortingKey,
		Settings:     ParseSettingsClause(rows[0].EngineFull),
	}

	cols, err := Query[struct {
		Name        string `json:"name"`
		InPartition U64    `json:"in_partition"`
	}](ctx, s.Client, `
SELECT name, is_in_partition_key AS in_partition FROM system.columns
WHERE database = {db:String} AND table = {table:String} ORDER BY position`, params)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, c := range cols {
		known[c.Name] = true
		if c.InPartition != 0 {
			info.MinMaxColumns = append(info.MinMaxColumns, c.Name)
		}
	}

	// Columns with a plain minmax skipping index also have per-part min/max.
	idx, err := Query[struct {
		Expr string `json:"expr"`
	}](ctx, s.Client, `
SELECT expr FROM system.data_skipping_indices
WHERE database = {db:String} AND table = {table:String} AND type = 'minmax'`, params)
	if err == nil {
		for _, i := range idx {
			name := strings.Trim(strings.TrimSpace(i.Expr), "`")
			if known[name] && !contains(info.MinMaxColumns, name) {
				info.MinMaxColumns = append(info.MinMaxColumns, name)
			}
		}
	}
	return info, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var settingsPair = regexp.MustCompile(`(\w+)\s*=\s*('(?:[^'\\]|\\.)*'|[^,\s]+)`)

// ParseSettingsClause reads "SETTINGS a = 1, b = 'x'" at the end of engine_full.
func ParseSettingsClause(engineFull string) map[string]string {
	out := map[string]string{}
	i := strings.LastIndex(engineFull, " SETTINGS ")
	if i < 0 {
		return out
	}
	for _, m := range settingsPair.FindAllStringSubmatch(engineFull[i+len(" SETTINGS "):], -1) {
		out[m[1]] = strings.Trim(m[2], "'")
	}
	return out
}

type Part struct {
	PartitionID           string `json:"partition_id"`
	Partition             string `json:"partition"`
	Name                  string `json:"name"`
	MinBlock              I64    `json:"min_block_number"`
	MaxBlock              I64    `json:"max_block_number"`
	Level                 U64    `json:"level"`
	DataVersion           U64    `json:"data_version"`
	Rows                  U64    `json:"rows"`
	Marks                 U64    `json:"marks"`
	BytesOnDisk           U64    `json:"bytes_on_disk"`
	DataCompressedBytes   U64    `json:"data_compressed_bytes"`
	DataUncompressedBytes U64    `json:"data_uncompressed_bytes"`
	PartType              string `json:"part_type"`
	DiskName              string `json:"disk_name"`
	ModificationTime      I64    `json:"modification_time"`
	MinDate               string `json:"min_date"`
	MaxDate               string `json:"max_date"`
	MinTime               I64    `json:"min_time"`
	MaxTime               I64    `json:"max_time"`

	// From system.part_log, when available.
	CreatedAt    I64    `json:"created_at,omitempty"`
	CreatedEvent string `json:"created_event,omitempty"`
	DurationMs   U64    `json:"duration_ms,omitempty"`
	MergedFrom   U64    `json:"merged_from,omitempty"`
}

func (s *Store) Parts(ctx context.Context, database, table, host string) ([]Part, error) {
	params := map[string]string{"db": database, "table": table}
	return Query[Part](ctx, s.Client, `
SELECT partition_id, partition, name, min_block_number, max_block_number, level, data_version,
    rows, marks, bytes_on_disk, data_compressed_bytes, data_uncompressed_bytes, part_type, disk_name,
    toUnixTimestamp(modification_time) AS modification_time,
    toString(min_date) AS min_date, toString(max_date) AS max_date,
    toUnixTimestamp(min_time) AS min_time, toUnixTimestamp(max_time) AS max_time
FROM `+s.src("system.parts")+`
WHERE active AND database = {db:String} AND table = {table:String}`+s.hostFilter(host, params)+`
ORDER BY partition_id, min_block_number, max_block_number`, params)
}

type PartLogEntry struct {
	PartName   string `json:"part_name"`
	CreatedAt  I64    `json:"created_at"`
	Event      string `json:"event"`
	DurationMs U64    `json:"duration_ms"`
	MergedFrom U64    `json:"merged_from"`
}

func (s *Store) HasPartLog(ctx context.Context) bool {
	rows, err := Query[struct{}](ctx, s.Client, "SELECT 1 FROM system.tables WHERE database = 'system' AND name = 'part_log'", nil)
	return err == nil && len(rows) > 0
}

// PartLog returns the event that created each active part. The event_date
// bound keeps the scan to days that can hold an active part.
func (s *Store) PartLog(ctx context.Context, database, table, host string) ([]PartLogEntry, error) {
	params := map[string]string{"db": database, "table": table}
	hf := s.hostFilter(host, params)
	return Query[PartLogEntry](ctx, s.Client, `
SELECT part_name,
    toUnixTimestamp(max(event_time)) AS created_at,
    toString(argMax(event_type, event_time)) AS event,
    argMax(duration_ms, event_time) AS duration_ms,
    argMax(length(merged_from), event_time) AS merged_from
FROM `+s.src("system.part_log")+`
WHERE database = {db:String} AND table = {table:String}
    AND event_type IN ('NewPart', 'MergeParts', 'MutatePart', 'DownloadPart')
    AND event_date >= (SELECT toDate(min(modification_time)) - 1 FROM system.parts WHERE active AND database = {db:String} AND table = {table:String})
    AND part_name IN (SELECT name FROM system.parts WHERE active AND database = {db:String} AND table = {table:String})`+hf+`
GROUP BY part_name`, params)
}

type Merge struct {
	PartitionID              string   `json:"partition_id"`
	ResultPartName           string   `json:"result_part_name"`
	SourcePartNames          []string `json:"source_part_names"`
	NumParts                 U64      `json:"num_parts"`
	Elapsed                  float64  `json:"elapsed"`
	Progress                 float64  `json:"progress"`
	TotalSizeBytesCompressed U64      `json:"total_size_bytes_compressed"`
	BytesReadUncompressed    U64      `json:"bytes_read_uncompressed"`
	BytesWrittenUncompressed U64      `json:"bytes_written_uncompressed"`
	RowsRead                 U64      `json:"rows_read"`
	MergeType                string   `json:"merge_type"`
	MergeAlgorithm           string   `json:"merge_algorithm"`
	IsMutation               U64      `json:"is_mutation"`
}

func (s *Store) Merges(ctx context.Context, database, table, host string) ([]Merge, error) {
	params := map[string]string{"db": database, "table": table}
	return Query[Merge](ctx, s.Client, `
SELECT partition_id, result_part_name, source_part_names, num_parts, elapsed, progress,
    total_size_bytes_compressed, bytes_read_uncompressed, bytes_written_uncompressed, rows_read,
    toString(merge_type) AS merge_type, toString(merge_algorithm) AS merge_algorithm, is_mutation
FROM `+s.src("system.merges")+`
WHERE database = {db:String} AND table = {table:String}`+s.hostFilter(host, params)+`
ORDER BY elapsed DESC`, params)
}

type QueuedMerge struct {
	NewPartName          string   `json:"new_part_name"`
	PartsToMerge         []string `json:"parts_to_merge"`
	CreateTime           I64      `json:"create_time"`
	NumTries             U64      `json:"num_tries"`
	LastException        string   `json:"last_exception"`
	PostponeReason       string   `json:"postpone_reason"`
	IsCurrentlyExecuting U64      `json:"is_currently_executing"`
}

func (s *Store) Queue(ctx context.Context, database, table, host string) ([]QueuedMerge, error) {
	params := map[string]string{"db": database, "table": table}
	return Query[QueuedMerge](ctx, s.Client, `
SELECT new_part_name, parts_to_merge, toUnixTimestamp(create_time) AS create_time, num_tries,
    last_exception, postpone_reason, is_currently_executing
FROM `+s.src("system.replication_queue")+`
WHERE database = {db:String} AND table = {table:String} AND type = 'MERGE_PARTS'`+s.hostFilter(host, params)+`
ORDER BY create_time`, params)
}

// MergeTreeSettings returns server defaults for names, overridden by the
// table's own SETTINGS clause.
func (s *Store) MergeTreeSettings(ctx context.Context, names []string, table map[string]string) (map[string]string, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = Literal(n)
	}
	rows, err := Query[struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}](ctx, s.Client, "SELECT name, value FROM system.merge_tree_settings WHERE name IN ("+strings.Join(quoted, ", ")+")", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Name] = r.Value
	}
	for _, n := range names {
		if v, ok := table[n]; ok {
			out[n] = v
		}
	}
	return out, nil
}

type MinMax struct {
	Part string   `json:"part"`
	Min  []string `json:"min"`
	Max  []string `json:"max"`
}

// MinMax reads the min and max of columns for each part in one partition.
// Column names must already be checked against system.columns.
func (s *Store) MinMax(ctx context.Context, info *TableInfo, host, partitionID string, columns []string) ([]MinMax, error) {
	if len(columns) == 0 {
		return nil, nil
	}
	mins := make([]string, len(columns))
	maxs := make([]string, len(columns))
	for i, c := range columns {
		mins[i] = "toString(min(" + Ident(c) + "))"
		maxs[i] = "toString(max(" + Ident(c) + "))"
	}
	params := map[string]string{"pid": partitionID}
	from := Ident(info.Database) + "." + Ident(info.Name)
	if s.Cluster != "" {
		from = fmt.Sprintf("clusterAllReplicas(%s, %s)", Literal(s.Cluster), from)
	}
	rows, err := Query[MinMax](ctx, s.Client, fmt.Sprintf(`
SELECT _part AS part, [%s] AS min, [%s] AS max
FROM %s
WHERE _partition_id = {pid:String}%s
GROUP BY _part`, strings.Join(mins, ", "), strings.Join(maxs, ", "), from, s.hostFilter(host, params)), params)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Part < rows[j].Part })
	return rows, nil
}
