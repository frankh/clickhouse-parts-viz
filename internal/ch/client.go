// Package ch is a small client for the ClickHouse HTTP interface.
package ch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base     *url.URL
	user     string
	password string
	http     *http.Client
	// MaxExecutionTime is sent as a query setting when > 0. It needs a user
	// with readonly=0 or readonly=2.
	MaxExecutionTime int
}

func New(rawURL, user, password string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse CLICKHOUSE_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("CLICKHOUSE_URL must be http:// or https://, got %q", u.Scheme)
	}
	if u.User != nil {
		if user == "" {
			user = u.User.Username()
		}
		if p, ok := u.User.Password(); ok && password == "" {
			password = p
		}
		u.User = nil
	}
	return &Client{base: u, user: user, password: password, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

// Query runs sql with FORMAT JSONEachRow and decodes each row into a new T.
// params are bound to {name:Type} placeholders in sql.
func Query[T any](ctx context.Context, c *Client, sql string, params map[string]string) ([]T, error) {
	q := c.base.Query()
	for k, v := range params {
		q.Set("param_"+k, v)
	}
	if c.MaxExecutionTime > 0 {
		q.Set("max_execution_time", strconv.Itoa(c.MaxExecutionTime))
	}
	u := *c.base
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(sql+"\nFORMAT JSONEachRow"))
	if err != nil {
		return nil, err
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.password != "" {
		req.Header.Set("X-ClickHouse-Key", c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("clickhouse: %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	var out []T
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row T
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("decode row: %w: %s", err, truncate(line, 200))
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// U64 decodes a UInt64 that ClickHouse may send as a JSON string.
type U64 uint64

func (u *U64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*u = 0
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*u = U64(n)
	return nil
}

// I64 decodes an Int64 that ClickHouse may send as a JSON string.
type I64 int64

func (i *I64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*i = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = I64(n)
	return nil
}

// Ident quotes a ClickHouse identifier with backticks.
func Ident(s string) string {
	return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(s) + "`"
}

// Literal quotes a ClickHouse string literal.
func Literal(s string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'"
}
