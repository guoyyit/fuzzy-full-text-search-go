package fuzzsearch

// levenshtein returns the Levenshtein edit distance between a and b
// (insert, delete, substitute each cost 1). It operates on bytes, which
// is exact because all terms are produced from Unicode letters/digits
// that survive whole under single-byte edits after case folding; for
// full correctness on multibyte terms we compare rune slices via
// distanceRunes.
func levenshtein(a, b string) int {
	return distanceRunes([]rune(a), []rune(b))
}

func distanceRunes(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// withinEditDistance reports whether distance(a,b) <= maxEd, returning
// the exact distance when it is. It prunes DP cells outside the
// |i-j| <= maxEd band and bails out when a whole row stays above maxEd.
func withinEditDistance(a, b string, maxEd int) (int, bool) {
	ra, rb := []rune(a), []rune(b)
	// Length difference is a lower bound on the distance.
	d := len(ra) - len(rb)
	if d < 0 {
		d = -d
	}
	if d > maxEd {
		return 0, false
	}
	if maxEd == 0 {
		if a == b {
			return 0, true
		}
		return 0, false
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := range curr {
			curr[j] = maxEd + 1
		}
		curr[0] = i
		lo := i - maxEd
		if lo < 1 {
			lo = 1
		}
		hi := i + maxEd
		if hi > len(rb) {
			hi = len(rb)
		}
		rowMin := maxEd + 1
		for j := lo; j <= hi; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			v := prev[j] + 1 // delete
			if x := curr[j-1] + 1; x < v {
				v = x // insert
			}
			if x := prev[j-1] + cost; x < v {
				v = x // substitute/match
			}
			curr[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > maxEd {
			return 0, false
		}
		prev, curr = curr, prev
	}
	dist := prev[len(rb)]
	if dist <= maxEd {
		return dist, true
	}
	return 0, false
}

func minInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
