package search

import "github.com/guoyyit/fuzzy-full-text-search-go/internal/editdist"

func editDistanceBounded(a, b string, max int) int {
	return editdist.Distance(a, b, max)
}
