package selector

import (
	"fmt"
	"math"
	"testing"
)

const mib = 1024 * 1024

func mkParts(sizes []uint64, age int64) []Part {
	parts := make([]Part, len(sizes))
	for i, s := range sizes {
		parts[i] = Part{Name: fmt.Sprintf("all_%d_%d_0", i+1, i+1), PartitionID: "all", Size: s, Rows: s / 100, Age: age}
	}
	return parts
}

func equalSizes(n int, size uint64) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = size
	}
	return out
}

func unlimited(n int) []Constraint {
	c := make([]Constraint, n)
	for i := range c {
		c[i] = Constraint{MaxSizeBytes: 150 << 30, MaxSizeRows: math.MaxUint64}
	}
	return c
}

func run(parts []Part, n int, s Settings) []Candidate {
	stats := map[string]PartitionStats{"all": {PartCount: len(parts), MinAge: parts[0].Age}}
	return Select([][]Part{parts}, stats, unlimited(n), s)
}

func TestEqualSmallPartsMergeAll(t *testing.T) {
	got := run(mkParts(equalSizes(10, mib), 0), 3, DefaultSettings())
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(got))
	}
	if len(got[0].Parts) != 10 {
		t.Fatalf("want all 10 parts, got %d", len(got[0].Parts))
	}
}

func TestTooFewFreshPartsDoNotMerge(t *testing.T) {
	// 4 equal parts < base 5 and too young to lower the base.
	if got := run(mkParts(equalSizes(4, mib), 0), 1, DefaultSettings()); len(got) != 0 {
		t.Fatalf("want no candidates, got %+v", got)
	}
}

func TestHugePartNotMergedWithSmallOnes(t *testing.T) {
	sizes := append([]uint64{10 << 30}, equalSizes(4, mib)...)

	if got := run(mkParts(sizes, 0), 1, DefaultSettings()); len(got) != 0 {
		t.Fatalf("fresh parts: want no candidates, got %+v", got)
	}

	// Old parts lower the base to 2: the small parts merge, the huge part stays out.
	got := run(mkParts(sizes, 60*86400), 1, DefaultSettings())
	if len(got) != 1 {
		t.Fatalf("old parts: want 1 candidate, got %d", len(got))
	}
	for _, name := range got[0].Parts {
		if name == "all_1_1_0" {
			t.Fatalf("huge part must not be in the merge: %v", got[0].Parts)
		}
	}
	if len(got[0].Parts) != 4 {
		t.Fatalf("want the 4 small parts, got %v", got[0].Parts)
	}
}

func TestMaxPartsToMergeAtOnce(t *testing.T) {
	parts := mkParts(equalSizes(150, mib), 0)
	got := run(parts, 2, DefaultSettings())
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(got))
	}
	// The lower-max-parts heuristic truncates 100 to 99 at 150 parts.
	if len(got[0].Parts) != 99 || got[0].Parts[0] != parts[51].Name {
		t.Fatalf("first pick: want rightmost 99 parts, got %d starting %s", len(got[0].Parts), got[0].Parts[0])
	}
	if len(got[1].Parts) != 51 || got[1].Parts[0] != parts[0].Name {
		t.Fatalf("second pick: want first 51 parts, got %d starting %s", len(got[1].Parts), got[1].Parts[0])
	}
}

func TestSizeConstraint(t *testing.T) {
	parts := mkParts(equalSizes(10, 100*mib), 0)
	stats := map[string]PartitionStats{"all": {PartCount: 10}}
	got := Select([][]Part{parts}, stats, []Constraint{{MaxSizeBytes: 500 * mib, MaxSizeRows: math.MaxUint64}}, DefaultSettings())
	if len(got) != 1 || len(got[0].Parts) != 5 || got[0].SumBytes > 500*mib {
		t.Fatalf("want 5 parts within 500MiB, got %+v", got)
	}
}

func TestRangesAreIndependent(t *testing.T) {
	a := mkParts(equalSizes(6, mib), 0)
	b := mkParts(equalSizes(6, mib), 0)
	for i := range b {
		b[i].PartitionID = "p2"
		b[i].Name = "p2" + b[i].Name[3:]
	}
	stats := map[string]PartitionStats{"all": {PartCount: 6}, "p2": {PartCount: 6}}
	got := Select([][]Part{a, b}, stats, unlimited(5), DefaultSettings())
	if len(got) != 2 || got[0].PartitionID == got[1].PartitionID {
		t.Fatalf("want one candidate per partition, got %+v", got)
	}
}

func TestApplySettings(t *testing.T) {
	s := DefaultSettings()
	s.Apply(map[string]string{"merge_selector_base": "3.5", "max_parts_to_merge_at_once": "7", "merge_selector_enable_heuristic_to_lower_max_parts_to_merge_at_once": "0", "junk": "x"})
	if s.Base != 3.5 || s.MaxPartsToMergeAtOnce != 7 || s.EnableHeuristicToLowerMaxPartsToMergeAtOnce {
		t.Fatalf("settings not applied: %+v", s)
	}
}
