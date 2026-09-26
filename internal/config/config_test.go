package config

import "testing"

func TestDefaultValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
}

func TestInvalid(t *testing.T) {
	bad := []Config{
		{MaxEditDistance: -1, DefaultFieldWeight: 1, FuzzyPenalty: 0.3, PhraseProximityBonus: 0.5},
		{MaxEditDistance: 11, DefaultFieldWeight: 1, FuzzyPenalty: 0.3, PhraseProximityBonus: 0.5},
		{MaxEditDistance: 1, DefaultFieldWeight: -1, FuzzyPenalty: 0.3, PhraseProximityBonus: 0.5},
		{MaxEditDistance: 1, DefaultFieldWeight: 1, FuzzyPenalty: 0, PhraseProximityBonus: 0.5},
		{MaxEditDistance: 1, DefaultFieldWeight: 1, FuzzyPenalty: 0.3, PhraseProximityBonus: 2},
		Default(),
	}
	bad[5].FieldWeights = map[string]float64{"title": -2}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d expected error", i)
		}
	}
}

func TestWeight(t *testing.T) {
	c := Default()
	c.FieldWeights["title"] = 3
	if c.Weight("title") != 3 || c.Weight("body") != 1 {
		t.Fatal("weight lookup wrong")
	}
}
