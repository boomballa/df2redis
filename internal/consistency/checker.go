package consistency

import (
	"fmt"

	"df2redis/internal/redisx"
)

// Mismatch captures differences between source and target.
type Mismatch struct {
	Key         string `json:"key"`
	SourceValue string `json:"sourceValue"`
	TargetValue string `json:"targetValue"`
}

// CompareStringValues compares simple string keys between source and target.
func CompareStringValues(source, target *redisx.Client, keys []string) ([]Mismatch, error) {
	mismatches := make([]Mismatch, 0)
	for _, key := range keys {
		srcVal, err := source.GetString(key)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s from source: %w", key, err)
		}
		tgtVal, err := target.GetString(key)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s from target: %w", key, err)
		}
		if srcVal != tgtVal {
			mismatches = append(mismatches, Mismatch{
				Key:         key,
				SourceValue: srcVal,
				TargetValue: tgtVal,
			})
		}
	}
	return mismatches, nil
}
