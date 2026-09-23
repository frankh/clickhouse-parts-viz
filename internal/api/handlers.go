package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/frankh/clickhouse-parts-viz/internal/ch"
	"github.com/frankh/clickhouse-parts-viz/internal/selector"
)

type Server struct {
	Store *ch.Store

	mu     sync.Mutex
	minmax map[string]minmaxEntry
}

type minmaxEntry struct {
	at   time.Time
	rows []ch.MinMax
}

const minmaxTTL = 30 * time.Second

func New(store *ch.Store) *Server {
	return &Server{Store: store, minmax: map[string]minmaxEntry{}}
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("GET /api/tables", s.tables)
	mux.HandleFunc("GET /api/parts", s.parts)
	mux.HandleFunc("GET /api/minmax", s.minMax)
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		status := http.StatusBadGateway
		var he httpError
		if errors.As(err, &he) {
			status = he.status
		} else {
			slog.Error("request failed", "err", err)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, map[string]string{"status": "ok"}, s.Store.Ping(ctx))
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Store.Hosts(r.Context())
	writeJSON(w, map[string]any{"cluster": s.Store.Cluster, "hosts": hosts}, err)
}

func (s *Server) tables(w http.ResponseWriter, r *http.Request) {
	tables, err := s.Store.Tables(r.Context())
	writeJSON(w, tables, err)
}

// table checks the database/table query params against system.tables.
func (s *Server) table(r *http.Request) (*ch.TableInfo, string, error) {
	q := r.URL.Query()
	db, name := q.Get("database"), q.Get("table")
	if db == "" || name == "" {
		return nil, "", httpError{http.StatusBadRequest, "database and table are required"}
	}
	info, err := s.Store.Table(r.Context(), db, name)
	if err != nil {
		return nil, "", err
	}
	if info == nil {
		return nil, "", httpError{http.StatusBadRequest, "unknown MergeTree table " + db + "." + name}
	}
	if s.Store.Cluster == "" {
		return info, "", nil
	}
	// Parts are per replica, so cluster mode always reads one host.
	hosts, err := s.Store.Hosts(r.Context())
	if err != nil {
		return nil, "", err
	}
	host := q.Get("host")
	if host == "" && len(hosts) > 0 {
		host = hosts[0]
	}
	if !slices.Contains(hosts, host) {
		return nil, "", httpError{http.StatusBadRequest, "unknown host " + host}
	}
	return info, host, nil
}

type partsResponse struct {
	Table        *ch.TableInfo        `json:"table"`
	Host         string               `json:"host,omitempty"`
	Now          int64                `json:"now"`
	PartLog      bool                 `json:"part_log"`
	Parts        []ch.Part            `json:"parts"`
	Merges       []ch.Merge           `json:"merges"`
	Queued       []ch.QueuedMerge     `json:"queued"`
	Candidates   []selector.Candidate `json:"candidates"`
	Settings     map[string]string    `json:"settings"`
	SelectorNote string               `json:"selector_note,omitempty"`
	Warnings     []string             `json:"warnings,omitempty"`
}

func (s *Server) parts(w http.ResponseWriter, r *http.Request) {
	resp, err := s.buildParts(r)
	writeJSON(w, resp, err)
}

func (s *Server) buildParts(r *http.Request) (*partsResponse, error) {
	info, host, err := s.table(r)
	if err != nil {
		return nil, err
	}
	limit := 20
	if v := r.URL.Query().Get("candidates"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 500 {
			return nil, httpError{http.StatusBadRequest, "candidates must be 0..500"}
		}
		limit = n
	}

	ctx := r.Context()
	db, tbl := info.Database, info.Name
	resp := &partsResponse{Table: info, Host: host, Merges: []ch.Merge{}, Queued: []ch.QueuedMerge{}, Candidates: []selector.Candidate{}}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		partLog  []ch.PartLogEntry
	)
	run := func(required bool, name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				defer mu.Unlock()
				if required && firstErr == nil {
					firstErr = err
				} else if !required {
					resp.Warnings = append(resp.Warnings, name+": "+err.Error())
				}
			}
		}()
	}

	run(true, "now", func() error {
		rows, err := ch.Query[struct {
			Now ch.I64 `json:"now"`
		}](ctx, s.Store.Client, "SELECT toUnixTimestamp(now()) AS now", nil)
		if err == nil && len(rows) > 0 {
			resp.Now = int64(rows[0].Now)
		}
		return err
	})
	run(true, "parts", func() (err error) {
		resp.Parts, err = s.Store.Parts(ctx, db, tbl, host)
		return err
	})
	run(false, "merges", func() error {
		m, err := s.Store.Merges(ctx, db, tbl, host)
		if m != nil {
			resp.Merges = m
		}
		return err
	})
	if strings.HasPrefix(info.Engine, "Replicated") || strings.HasPrefix(info.Engine, "Shared") {
		run(false, "replication_queue", func() error {
			q, err := s.Store.Queue(ctx, db, tbl, host)
			if q != nil {
				resp.Queued = q
			}
			return err
		})
	}
	if s.Store.HasPartLog(ctx) {
		resp.PartLog = true
		run(false, "part_log", func() (err error) {
			partLog, err = s.Store.PartLog(ctx, db, tbl, host)
			return err
		})
	}
	run(false, "merge_tree_settings", func() (err error) {
		resp.Settings, err = s.Store.MergeTreeSettings(ctx, selector.SettingNames, info.Settings)
		return err
	})
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if resp.Parts == nil {
		resp.Parts = []ch.Part{}
	}

	byName := make(map[string]*ch.Part, len(resp.Parts))
	for i := range resp.Parts {
		byName[resp.Parts[i].Name] = &resp.Parts[i]
	}
	for _, e := range partLog {
		if p := byName[e.PartName]; p != nil {
			p.CreatedAt, p.CreatedEvent, p.DurationMs, p.MergedFrom = e.CreatedAt, e.Event, e.DurationMs, e.MergedFrom
		}
	}

	resp.Candidates, resp.SelectorNote = candidates(resp, limit)
	return resp, nil
}

// candidates splits parts into mergeable ranges the way ClickHouse does
// (per partition, broken at parts already in a merge) and runs the selector.
func candidates(resp *partsResponse, limit int) ([]selector.Candidate, string) {
	settings := selector.DefaultSettings()
	settings.Apply(resp.Settings)

	note := ""
	switch algo := resp.Settings["merge_selector_algorithm"]; algo {
	case "", "Simple":
	case "StochasticSimple":
		note = "Table uses StochasticSimple; candidates use Simple without the random parts."
	default:
		note = "Table uses the " + algo + " merge selector; candidates show what Simple would pick."
	}

	busy := map[string]bool{}
	for _, m := range resp.Merges {
		for _, n := range m.SourcePartNames {
			busy[n] = true
		}
	}
	for _, q := range resp.Queued {
		for _, n := range q.PartsToMerge {
			busy[n] = true
		}
	}

	stats := map[string]selector.PartitionStats{}
	var ranges [][]selector.Part
	var cur []selector.Part
	flush := func() {
		if len(cur) > 1 {
			ranges = append(ranges, cur)
		}
		cur = nil
	}
	for _, p := range resp.Parts {
		age := max(resp.Now-int64(p.ModificationTime), 0)
		st, ok := stats[p.PartitionID]
		if !ok {
			st.MinAge = math.MaxInt64
		}
		st.PartCount++
		st.MinAge = min(st.MinAge, age)
		stats[p.PartitionID] = st

		if len(cur) > 0 && cur[0].PartitionID != p.PartitionID {
			flush()
		}
		if busy[p.Name] {
			flush()
			continue
		}
		cur = append(cur, selector.Part{Name: p.Name, PartitionID: p.PartitionID, Size: uint64(p.BytesOnDisk), Rows: uint64(p.Rows), Age: age})
	}
	flush()

	maxBytes := uint64(150 << 30)
	if v, err := strconv.ParseUint(resp.Settings["max_bytes_to_merge_at_max_space_in_pool"], 10, 64); err == nil && v > 0 {
		maxBytes = v
	}
	constraints := make([]selector.Constraint, limit)
	for i := range constraints {
		constraints[i] = selector.Constraint{MaxSizeBytes: maxBytes, MaxSizeRows: math.MaxUint64}
	}
	out := selector.Select(ranges, stats, constraints, settings)
	if out == nil {
		out = []selector.Candidate{}
	}
	return out, note
}

func (s *Server) minMax(w http.ResponseWriter, r *http.Request) {
	info, host, err := s.table(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	pid := r.URL.Query().Get("partition_id")
	if pid == "" {
		writeJSON(w, nil, httpError{http.StatusBadRequest, "partition_id is required"})
		return
	}
	key := strings.Join([]string{host, info.Database, info.Name, pid}, "\x00")

	s.mu.Lock()
	e, ok := s.minmax[key]
	s.mu.Unlock()
	if !ok || time.Since(e.at) > minmaxTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		rows, err := s.Store.MinMax(ctx, info, host, pid, info.MinMaxColumns)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		if rows == nil {
			rows = []ch.MinMax{}
		}
		e = minmaxEntry{at: time.Now(), rows: rows}
		s.mu.Lock()
		if len(s.minmax) > 1000 {
			clear(s.minmax)
		}
		s.minmax[key] = e
		s.mu.Unlock()
	}
	writeJSON(w, map[string]any{"columns": info.MinMaxColumns, "parts": e.rows}, nil)
}
