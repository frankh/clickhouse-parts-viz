// Package selector is a port of ClickHouse's SimpleMergeSelector
// (src/Storages/MergeTree/Compaction/MergeSelectors/SimpleMergeSelector.cpp).
//
// The stochastic parts of the original (blurry base, stochastic sliding) are
// left out so the output is deterministic.
package selector

import (
	"math"
	"sort"
)

type Part struct {
	Name        string
	PartitionID string
	Size        uint64 // bytes on disk
	Rows        uint64
	Age         int64 // seconds since modification_time
}

type PartitionStats struct {
	PartCount int
	MinAge    int64
}

type Constraint struct {
	MaxSizeBytes uint64
	MaxSizeRows  uint64
}

type Settings struct {
	MaxPartsToMergeAtOnce int
	MinPartsToMergeAtOnce int
	PartsToThrowInsert    int
	Base                  float64
	WindowSize            int

	MinSizeToLowerBase         uint64
	MaxSizeToLowerBase         uint64
	MinAgeToLowerBaseAtMinSize int64
	MinAgeToLowerBaseAtMaxSize int64
	MaxAgeToLowerBaseAtMinSize int64
	MaxAgeToLowerBaseAtMaxSize int64
	MinPartsToLowerBase        int
	MaxPartsToLowerBase        int
	SizeFixedCostToAdd         uint64

	EnableHeuristicToAlignParts                           bool
	HeuristicToAlignPartsMinRatioOfSumSizeToPrevPart      float64
	HeuristicToAlignPartsMaxAbsoluteDifferenceInPowersOf2 float64
	HeuristicToAlignPartsMaxScoreAdjustment               float64

	MinAgeToForceMerge          int64
	MinPartitionAgeToForceMerge int64

	EnableHeuristicToRemoveSmallPartsAtRight   bool
	HeuristicToRemoveSmallPartsAtRightMaxRatio float64

	EnableHeuristicToLowerMaxPartsToMergeAtOnce   bool
	HeuristicToLowerMaxPartsToMergeAtOnceExponent int
}

// DefaultSettings returns the defaults of SimpleMergeSelector::Settings.
func DefaultSettings() Settings {
	return Settings{
		MaxPartsToMergeAtOnce:      100,
		PartsToThrowInsert:         3000,
		Base:                       5,
		WindowSize:                 1000,
		MinSizeToLowerBase:         1024 * 1024,
		MaxSizeToLowerBase:         100 * 1024 * 1024 * 1024,
		MinAgeToLowerBaseAtMinSize: 10,
		MinAgeToLowerBaseAtMaxSize: 10,
		MaxAgeToLowerBaseAtMinSize: 3600,
		MaxAgeToLowerBaseAtMaxSize: 30 * 86400,
		MinPartsToLowerBase:        10,
		MaxPartsToLowerBase:        50,
		SizeFixedCostToAdd:         5 * 1024 * 1024,

		EnableHeuristicToAlignParts:                           true,
		HeuristicToAlignPartsMinRatioOfSumSizeToPrevPart:      0.9,
		HeuristicToAlignPartsMaxAbsoluteDifferenceInPowersOf2: 0.5,
		HeuristicToAlignPartsMaxScoreAdjustment:               0.75,

		EnableHeuristicToRemoveSmallPartsAtRight:   true,
		HeuristicToRemoveSmallPartsAtRightMaxRatio: 0.01,

		EnableHeuristicToLowerMaxPartsToMergeAtOnce:   true,
		HeuristicToLowerMaxPartsToMergeAtOnceExponent: 5,
	}
}

// Candidate is a range of parts the selector would merge.
type Candidate struct {
	Rank        int      `json:"rank"`
	PartitionID string   `json:"partition_id"`
	Parts       []string `json:"parts"`
	SumBytes    uint64   `json:"sum_bytes"`
	SumRows     uint64   `json:"sum_rows"`
	Score       float64  `json:"score"`
}

type scoredRange struct {
	rangeIdx   int
	begin, end int
	bytes      uint64
	rows       uint64
	score      float64
}

func interpolateLinear(min, max, ratio float64) float64 {
	return min + (max-min)*ratio
}

func mapPiecewiseLinearToUnit(value, min, max float64) float64 {
	if value <= min {
		return 0
	}
	if value >= max {
		return 1
	}
	return (value - min) / (max - min)
}

func score(count, sumSize, sumSizeFixedCost float64) float64 {
	return (sumSize + sumSizeFixedCost*count) / (count - 1.9)
}

func allow(sumSize, maxSize, minAge, minPartitionAge, partitionSize, minSizeLog, maxSizeLog float64, size int, s *Settings) bool {
	if s.MinAgeToForceMerge != 0 && minAge >= float64(s.MinAgeToForceMerge) {
		return true
	}
	if s.MinPartitionAgeToForceMerge != 0 && minPartitionAge > 0 && minPartitionAge >= float64(s.MinPartitionAgeToForceMerge) {
		return true
	}
	if s.MinPartsToMergeAtOnce != 0 && size < s.MinPartsToMergeAtOnce {
		return false
	}

	sizeNormalized := mapPiecewiseLinearToUnit(math.Log(1+sumSize), minSizeLog, maxSizeLog)
	minAgeToLowerBase := interpolateLinear(float64(s.MinAgeToLowerBaseAtMinSize), float64(s.MinAgeToLowerBaseAtMaxSize), sizeNormalized)
	maxAgeToLowerBase := interpolateLinear(float64(s.MaxAgeToLowerBaseAtMinSize), float64(s.MaxAgeToLowerBaseAtMaxSize), sizeNormalized)
	ageNormalized := mapPiecewiseLinearToUnit(minAge, minAgeToLowerBase, maxAgeToLowerBase)
	numPartsNormalized := mapPiecewiseLinearToUnit(partitionSize, float64(s.MinPartsToLowerBase), float64(s.MaxPartsToLowerBase))
	combinedRatio := math.Min(1.0, ageNormalized+numPartsNormalized)
	loweredBase := interpolateLinear(s.Base, 2.0, combinedRatio)

	fixed := float64(s.SizeFixedCostToAdd)
	return (sumSize+float64(size)*fixed)/(maxSize+fixed) >= loweredBase
}

type estimator struct {
	s      *Settings
	ranges [][]Part
	scored []scoredRange
}

func (e *estimator) consider(rangeIdx, begin, end int, sumSize, sumRows, sizePrevAtLeft uint64) {
	parts := e.ranges[rangeIdx]
	s := e.s
	if s.EnableHeuristicToRemoveSmallPartsAtRight {
		var sizeDelta, rowsDelta uint64
		for end >= begin+3 && float64(parts[end-1].Size) < s.HeuristicToRemoveSmallPartsAtRightMaxRatio*float64(sumSize) {
			sizeDelta += parts[end-1].Size
			rowsDelta += parts[end-1].Rows
			end--
		}
		sumSize -= sizeDelta
		sumRows -= rowsDelta
	}

	current := score(float64(end-begin), float64(sumSize), float64(s.SizeFixedCostToAdd))

	if s.EnableHeuristicToAlignParts && float64(sizePrevAtLeft) > float64(sumSize)*s.HeuristicToAlignPartsMinRatioOfSumSizeToPrevPart {
		difference := math.Abs(math.Log2(float64(sumSize) / float64(sizePrevAtLeft)))
		if difference < s.HeuristicToAlignPartsMaxAbsoluteDifferenceInPowersOf2 {
			current *= interpolateLinear(s.HeuristicToAlignPartsMaxScoreAdjustment, 1,
				difference/s.HeuristicToAlignPartsMaxAbsoluteDifferenceInPowersOf2)
		}
	}

	e.scored = append(e.scored, scoredRange{rangeIdx, begin, end, sumSize, sumRows, current})
}

func (e *estimator) selectWithinPartsRange(rangeIdx int, constraint Constraint, stats map[string]PartitionStats, minSizeLog, maxSizeLog float64) {
	parts := e.ranges[rangeIdx]
	s := e.s
	partsCount := len(parts)
	if partsCount <= 1 {
		return
	}
	pstats := stats[parts[0].PartitionID]
	minPartitionAge := float64(pstats.MinAge)

	begin := 0
	if s.WindowSize > 0 && partsCount >= s.WindowSize {
		begin = partsCount - s.WindowSize
	}

	maxPartsToMergeAtOnce := s.MaxPartsToMergeAtOnce
	if s.MaxPartsToMergeAtOnce != 0 && s.EnableHeuristicToLowerMaxPartsToMergeAtOnce {
		switch {
		case float64(pstats.PartCount) < s.Base:
		case pstats.PartCount >= s.PartsToThrowInsert:
			maxPartsToMergeAtOnce = max(2, int(s.Base))
		default:
			exp := float64(s.HeuristicToLowerMaxPartsToMergeAtOnceExponent)
			fill := (float64(pstats.PartCount) - s.Base) / (float64(s.PartsToThrowInsert) - s.Base)
			maxPartsToMergeAtOnce = int(s.Base + (float64(maxPartsToMergeAtOnce)-s.Base)*(1.0-math.Pow(fill, exp)))
		}
		maxPartsToMergeAtOnce = min(maxPartsToMergeAtOnce, s.MaxPartsToMergeAtOnce)
	}

	for ; begin < partsCount; begin++ {
		sumSize := parts[begin].Size
		sumRows := parts[begin].Rows
		maxSize := parts[begin].Size
		minAge := parts[begin].Age

		for end := begin + 2; end <= partsCount; end++ {
			if maxPartsToMergeAtOnce != 0 && end-begin > maxPartsToMergeAtOnce {
				break
			}
			cur := parts[end-1]
			sumSize += cur.Size
			sumRows += cur.Rows
			maxSize = max(maxSize, cur.Size)
			minAge = min(minAge, cur.Age)

			if sumSize > constraint.MaxSizeBytes || sumRows > constraint.MaxSizeRows {
				break
			}

			if allow(float64(sumSize), float64(maxSize), float64(minAge), minPartitionAge, float64(partsCount),
				minSizeLog, maxSizeLog, end-begin, s) {
				var prev uint64
				if begin > 0 {
					prev = parts[begin-1].Size
				}
				e.consider(rangeIdx, begin, end, sumSize, sumRows, prev)
			}
		}
	}
}

// Select returns up to len(constraints) disjoint merge candidates, best first.
// Each element of ranges is a run of adjacent parts (same partition, sorted by
// block number) where any sub-range can be merged.
func Select(ranges [][]Part, stats map[string]PartitionStats, constraints []Constraint, s Settings) []Candidate {
	if len(constraints) == 0 {
		return nil
	}
	e := &estimator{s: &s, ranges: ranges}
	minSizeLog := math.Log(1 + float64(s.MinSizeToLowerBase))
	maxSizeLog := math.Log(1 + float64(s.MaxSizeToLowerBase))
	for i := range ranges {
		e.selectWithinPartsRange(i, constraints[0], stats, minSizeLog, maxSizeLog)
	}

	// Lowest score first; on a tie prefer the rightmost range (likely lower level).
	sort.SliceStable(e.scored, func(i, j int) bool {
		a, b := e.scored[i], e.scored[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if a.rangeIdx != b.rangeIdx {
			return a.rangeIdx > b.rangeIdx
		}
		return a.begin > b.begin
	})

	taken := map[int][][2]int{}
	overlaps := func(r scoredRange) bool {
		for _, t := range taken[r.rangeIdx] {
			if r.begin < t[1] && t[0] < r.end {
				return true
			}
		}
		return false
	}

	var out []Candidate
	next := 0
	for _, c := range constraints {
		found := false
		for ; next < len(e.scored); next++ {
			r := e.scored[next]
			if r.bytes <= c.MaxSizeBytes && r.rows <= c.MaxSizeRows && !overlaps(r) {
				taken[r.rangeIdx] = append(taken[r.rangeIdx], [2]int{r.begin, r.end})
				parts := ranges[r.rangeIdx][r.begin:r.end]
				names := make([]string, len(parts))
				for i, p := range parts {
					names[i] = p.Name
				}
				out = append(out, Candidate{
					Rank:        len(out) + 1,
					PartitionID: parts[0].PartitionID,
					Parts:       names,
					SumBytes:    r.bytes,
					SumRows:     r.rows,
					Score:       r.score,
				})
				next++
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	return out
}
