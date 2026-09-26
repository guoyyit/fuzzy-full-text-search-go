package fuzzsearch

import (
	"math"
	"strings"
	"testing"
)

func validCfg(t *testing.T) Config {
	return Config{
		FieldWeights:           map[string]float64{"title": 3, "body": 1},
		DefaultMaxEditDistance: 2,
		IDFWeight:              1,
		FuzzyPenalty:           0.5,
		FuzzyPrefixLength:      1,
		DataDir:                t.TempDir(),
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validCfg(t).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestConfigInvalid(t *testing.T) {
	bad := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"no fields", func(c *Config) { c.FieldWeights = nil }, "FieldWeights"},
		{"empty field name", func(c *Config) { c.FieldWeights[""] = 1 }, "non-empty"},
		{"zero weight", func(c *Config) { c.FieldWeights["title"] = 0 }, "> 0"},
		{"nan weight", func(c *Config) { c.FieldWeights["title"] = math.NaN() }, "> 0"},
		{"negative maxEd", func(c *Config) { c.DefaultMaxEditDistance = -1 }, "DefaultMaxEditDistance"},
		{"negative idf", func(c *Config) { c.IDFWeight = -1 }, "IDFWeight"},
		{"negative penalty", func(c *Config) { c.FuzzyPenalty = -0.1 }, "FuzzyPenalty"},
		{"negative prefix", func(c *Config) { c.FuzzyPrefixLength = -1 }, "FuzzyPrefixLength"},
		{"no datadir", func(c *Config) { c.DataDir = "" }, "DataDir"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			c := validCfg(t)
			tc.mut(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	c := validCfg(t)
	c.FieldWeights = nil
	if _, err := Open(c); err == nil {
		t.Fatal("Open with invalid config must fail")
	}
}
