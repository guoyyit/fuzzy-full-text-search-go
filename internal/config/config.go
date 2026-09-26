package config

import (
	"fmt"
	"sort"
	"strings"
)

// Config 是全文检索服务的全部可配置项。
//
// 相关度模型（在 search 包中使用）：
//   - 一次词条命中的基础得分 = 字段权重 ×（1 - FuzzyPenalty × 编辑距离）。
//   - 精确命中（编辑距离 0）不打折扣；近似命中按偏差线性扣分。
//   - 同字段内多次命中取最强（扣分最少）的一次，不重复计分。
//   - 一篇文档的总得分为各字段贡献之和；短语命中额外乘以
//     (1 + PhraseProximityBonus)，NOT 子句不贡献得分。
type Config struct {
	// MaxEditDistance 是近似命中允许的编辑距离上限（0 表示只接受精确命中）。
	MaxEditDistance int `json:"max_edit_distance"`
	// FieldWeights 为每个字段配置匹配得分权重；未列出的字段使用 DefaultFieldWeight。
	FieldWeights map[string]float64 `json:"field_weights"`
	// DefaultFieldWeight 是未显式配置字段的权重。
	DefaultFieldWeight float64 `json:"default_field_weight"`
	// FuzzyPenalty 是每单位编辑距离对命中得分的惩罚系数，取值 (0,1]。
	FuzzyPenalty float64 `json:"fuzzy_penalty"`
	// PhraseProximityBonus 是短语命中相对同词条逐词命中的额外加成，取值 [0,1]。
	PhraseProximityBonus float64 `json:"phrase_proximity_bonus"`
}

// Default 返回一份内置默认配置：
// 允许 1 个编辑距离的容错，默认字段权重 1.0，容错惩罚 0.3，短语加成 0.5。
func Default() Config {
	return Config{
		MaxEditDistance:      1,
		FieldWeights:         map[string]float64{},
		DefaultFieldWeight:   1.0,
		FuzzyPenalty:         0.3,
		PhraseProximityBonus: 0.5,
	}
}

// Weight 返回指定字段的有效权重。
func (c Config) Weight(field string) float64 {
	if w, ok := c.FieldWeights[field]; ok {
		return w
	}
	return c.DefaultFieldWeight
}

// Validate 校验配置；非法配置返回明确的错误原因。
func (c Config) Validate() error {
	var problems []string
	if c.MaxEditDistance < 0 {
		problems = append(problems, "max_edit_distance 不能为负数")
	}
	if c.MaxEditDistance > 10 {
		problems = append(problems, "max_edit_distance 不能大于 10（过宽的容错上限会使匹配失去意义并影响性能）")
	}
	if c.DefaultFieldWeight < 0 {
		problems = append(problems, "default_field_weight 不能为负数")
	}
	if c.FuzzyPenalty <= 0 || c.FuzzyPenalty > 1 {
		problems = append(problems, "fuzzy_penalty 必须在 (0,1] 区间内")
	}
	if c.PhraseProximityBonus < 0 || c.PhraseProximityBonus > 1 {
		problems = append(problems, "phrase_proximity_bonus 必须在 [0,1] 区间内")
	}
	fields := make([]string, 0, len(c.FieldWeights))
	for f, w := range c.FieldWeights {
		if f == "" {
			problems = append(problems, "field_weights 中不能包含空字段名")
		}
		if w < 0 {
			problems = append(problems, fmt.Sprintf("字段 %q 的权重不能为负数", f))
		}
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		if strings.TrimSpace(f) != f {
			problems = append(problems, fmt.Sprintf("字段名 %q 不能含有首尾空白", f))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("非法配置：%s", strings.Join(problems, "；"))
	}
	return nil
}
