package fuzzsearch

import (
	"errors"
	"fmt"
	"math"
)

// Config controls field weights, fuzzy-match limits and scoring parameters.
//
// All zero-value defaults (except FieldWeights and DataDir) are legal and
// mean: no fuzzy matching by default, pure term-frequency/field-weight
// scoring, no prefix requirement.
type Config struct {
	// FieldWeights maps a field name to its score multiplier.
	// Must be non-empty and every weight must be a finite number > 0.
	// Documents referencing a field absent from this map are accepted but
	// the field is neither searched nor scored.
	FieldWeights map[string]float64

	// DefaultMaxEditDistance is the global cap on edit distance for fuzzy
	// matches (query "term~"). Must be >= 0. A per-query "term~N" may
	// lower, but never raise, this cap.
	DefaultMaxEditDistance int

	// IDFWeight scales the inverse-document-frequency component of the
	// score: rare terms contribute more. Must be >= 0.
	IDFWeight float64

	// FuzzyPenalty is subtracted from a term's contribution per unit of
	// edit distance: score(ed=0) > score(ed=1) > ... . Must be >= 0.
	FuzzyPenalty float64

	// FuzzyPrefixLength requires the first N characters of a fuzzy query
	// term and a candidate term to be identical. Must be >= 0.
	FuzzyPrefixLength int

	// DataDir is where the write-ahead log (fuzzy-search.wal) lives.
	// The directory is created if missing. Must be non-empty.
	DataDir string
}

// Validate reports whether the configuration is legal, returning an
// error whose message names the offending field and why.
func (c Config) Validate() error {
	if len(c.FieldWeights) == 0 {
		return errors.New("config: FieldWeights must contain at least one field")
	}
	for name, w := range c.FieldWeights {
		if name == "" {
			return errors.New("config: field weight keys must be non-empty")
		}
		if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			return fmt.Errorf("config: weight for field %q must be a finite number > 0, got %v", name, w)
		}
	}
	if c.DefaultMaxEditDistance < 0 {
		return fmt.Errorf("config: DefaultMaxEditDistance must be >= 0, got %d", c.DefaultMaxEditDistance)
	}
	if math.IsNaN(c.IDFWeight) || math.IsInf(c.IDFWeight, 0) || c.IDFWeight < 0 {
		return fmt.Errorf("config: IDFWeight must be a finite number >= 0, got %v", c.IDFWeight)
	}
	if math.IsNaN(c.FuzzyPenalty) || math.IsInf(c.FuzzyPenalty, 0) || c.FuzzyPenalty < 0 {
		return fmt.Errorf("config: FuzzyPenalty must be a finite number >= 0, got %v", c.FuzzyPenalty)
	}
	if c.FuzzyPrefixLength < 0 {
		return fmt.Errorf("config: FuzzyPrefixLength must be >= 0, got %d", c.FuzzyPrefixLength)
	}
	if c.DataDir == "" {
		return errors.New("config: DataDir must be non-empty")
	}
	return nil
}
