package editdist

// Distance 返回 a 与 b 之间的 Levenshtein 编辑距离（插入/删除/替换各计 1）。
// 当真实距离大于 max 时，提前终止并返回 max+1（调用方可据此快速排除候选）。
// max < 0 时退化为计算完整距离。
func Distance(a, b string, max int) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	// 长度差本身就是距离下界，超界直接返回。
	diff := len(ra) - len(rb)
	if diff < 0 {
		diff = -diff
	}
	if max >= 0 && diff > max {
		return max + 1
	}

	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d := prev[j] + 1 // 删除
			if v := cur[j-1] + 1; v < d {
				d = v // 插入
			}
			if v := prev[j-1] + cost; v < d {
				d = v // 替换/匹配
			}
			cur[j] = d
			if d < rowMin {
				rowMin = d
			}
		}
		// 本行最小值已超界，后续行不可能再变小，提前终止。
		if max >= 0 && rowMin > max {
			return max + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
