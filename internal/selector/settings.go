package selector

import "strconv"

// SettingNames lists the MergeTree settings that feed the selector, as in
// fillSimpleSettings() in MergeSelectorApplier.cpp.
var SettingNames = []string{
	"merge_selector_algorithm",
	"merge_selector_window_size",
	"merge_selector_base",
	"merge_selector_enable_heuristic_to_remove_small_parts_at_right",
	"merge_selector_enable_heuristic_to_lower_max_parts_to_merge_at_once",
	"merge_selector_heuristic_to_lower_max_parts_to_merge_at_once_exponent",
	"max_parts_to_merge_at_once",
	"min_parts_to_merge_at_once",
	"parts_to_throw_insert",
	"min_age_to_force_merge_seconds",
	"min_age_to_force_merge_on_partition_only",
	"min_partition_age_to_force_merge_seconds",
	"max_bytes_to_merge_at_max_space_in_pool",
}

func parseBool(v string) (bool, bool) {
	switch v {
	case "1", "true", "True", "TRUE":
		return true, true
	case "0", "false", "False", "FALSE":
		return false, true
	}
	return false, false
}

// Apply overrides settings with MergeTree setting values (name -> value).
// Unknown names and bad values are ignored.
func (s *Settings) Apply(values map[string]string) {
	setInt := func(name string, dst *int) {
		if n, err := strconv.Atoi(values[name]); err == nil {
			*dst = n
		}
	}
	setInt64 := func(name string, dst *int64) {
		if n, err := strconv.ParseInt(values[name], 10, 64); err == nil {
			*dst = n
		}
	}
	setBool := func(name string, dst *bool) {
		if b, ok := parseBool(values[name]); ok {
			*dst = b
		}
	}

	setInt("merge_selector_window_size", &s.WindowSize)
	setInt("max_parts_to_merge_at_once", &s.MaxPartsToMergeAtOnce)
	setInt("min_parts_to_merge_at_once", &s.MinPartsToMergeAtOnce)
	setInt("parts_to_throw_insert", &s.PartsToThrowInsert)
	setInt("merge_selector_heuristic_to_lower_max_parts_to_merge_at_once_exponent", &s.HeuristicToLowerMaxPartsToMergeAtOnceExponent)
	setBool("merge_selector_enable_heuristic_to_remove_small_parts_at_right", &s.EnableHeuristicToRemoveSmallPartsAtRight)
	// Servers without this setting also do not have the heuristic.
	if _, ok := values["merge_selector_enable_heuristic_to_lower_max_parts_to_merge_at_once"]; !ok {
		s.EnableHeuristicToLowerMaxPartsToMergeAtOnce = false
	}
	setBool("merge_selector_enable_heuristic_to_lower_max_parts_to_merge_at_once", &s.EnableHeuristicToLowerMaxPartsToMergeAtOnce)
	if f, err := strconv.ParseFloat(values["merge_selector_base"], 64); err == nil {
		s.Base = f
	}

	partitionOnly, _ := parseBool(values["min_age_to_force_merge_on_partition_only"])
	if !partitionOnly {
		setInt64("min_age_to_force_merge_seconds", &s.MinAgeToForceMerge)
	}
	setInt64("min_partition_age_to_force_merge_seconds", &s.MinPartitionAgeToForceMerge)
}
