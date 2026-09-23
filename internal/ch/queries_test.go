package ch

import "testing"

func TestParseSettingsClause(t *testing.T) {
	got := ParseSettingsClause("MergeTree PARTITION BY toYYYYMM(ts) ORDER BY (a, b) SETTINGS index_granularity = 8192, merge_selector_base = 3.5, storage_policy = 'hot, cold'")
	want := map[string]string{"index_granularity": "8192", "merge_selector_base": "3.5", "storage_policy": "hot, cold"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: want %q, got %q", k, v, got[k])
		}
	}
	if len(ParseSettingsClause("MergeTree ORDER BY a")) != 0 {
		t.Fatal("want no settings")
	}
}

func TestIdentAndLiteral(t *testing.T) {
	if got := Ident("a`b"); got != "`a\\`b`" {
		t.Fatalf("Ident: %s", got)
	}
	if got := Literal("it's"); got != `'it\'s'` {
		t.Fatalf("Literal: %s", got)
	}
}
