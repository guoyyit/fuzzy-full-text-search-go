package fuzzsearch

// Document is a uniquely identified collection of named text fields.
type Document struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// Highlight marks a [Start, End) byte interval inside one field value.
type Highlight struct {
	Field string `json:"field"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	// Term is the matched document term; EditDistance is the Levenshtein
	// distance to the query term (0 for exact matches).
	Term         string `json:"term"`
	EditDistance int    `json:"editDistance"`
}

// Hit is one ranked search result.
type Hit struct {
	DocID      string             `json:"docId"`
	Score      float64            `json:"score"`
	Highlights []Highlight        `json:"highlights"`
	Explain    map[string]float64 `json:"explain"`
}

// SearchResult is the deterministic, fully ordered answer to a query.
type SearchResult struct {
	Hits []Hit `json:"hits"`
}
