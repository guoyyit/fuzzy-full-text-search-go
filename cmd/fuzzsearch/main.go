// Command fuzzsearch runs the local, persistent fuzzy full-text search
// service over HTTP.
//
// Usage:
//
//	fuzzsearch -addr :8080 -data ./data -weights title=3,body=1 \
//	           -max-edit-distance 2 -fuzzy-penalty 0.5
//
// Endpoints:
//
//	GET    /v1/explain                 service configuration & semantics
//	PUT    /v1/documents/:id           upsert {"fields":{...}} (body id optional)
//	GET    /v1/documents/:id           fetch a document
//	DELETE /v1/documents/:id           delete a document
//	POST   /v1/batch                   [mutation...]
//	GET    /v1/search?q=...            search
//	POST   /v1/search                  {"query":"..."}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	fz "github.com/guoyyit/fuzzy-full-text-search-go"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dataDir := flag.String("data", "./fuzz-data", "directory for the persistent write-ahead log")
	weightsFlag := flag.String("weights", "title=3,body=1", "comma-separated field=weight pairs")
	maxEd := flag.Int("max-edit-distance", 2, "global fuzzy edit-distance cap (N in term~N)")
	idfWeight := flag.Float64("idf-weight", 1.0, "IDF component weight")
	fuzzyPenalty := flag.Float64("fuzzy-penalty", 0.5, "score subtracted implicitly per edit distance (1/(1+k*d))")
	fuzzyPrefix := flag.Int("fuzzy-prefix", 0, "required exact prefix length for fuzzy candidates")
	flag.Parse()

	weights, err := parseWeights(*weightsFlag)
	if err != nil {
		log.Fatalf("invalid -weights: %v", err)
	}
	cfg := fz.Config{
		FieldWeights:           weights,
		DefaultMaxEditDistance: *maxEd,
		IDFWeight:              *idfWeight,
		FuzzyPenalty:           *fuzzyPenalty,
		FuzzyPrefixLength:      *fuzzyPrefix,
		DataDir:                *dataDir,
	}
	svc, err := fz.Open(cfg)
	if err != nil {
		// Invalid configuration or unrecoverable storage error: refuse
		// to start with an explicit reason.
		log.Fatalf("startup: %v", err)
	}
	defer svc.Close()

	srv := &server{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/explain", srv.handleExplain)
	mux.HandleFunc("/v1/search", srv.handleSearch)
	mux.HandleFunc("/v1/batch", srv.handleBatch)
	mux.HandleFunc("/v1/documents/", srv.handleDocument)

	log.Printf("fuzzsearch listening on %s, data dir %s", *addr, *dataDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func parseWeights(s string) (map[string]float64, error) {
	weights := map[string]float64{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("expected field=weight, got %q", pair)
		}
		name := pair[:eq]
		w, err := strconv.ParseFloat(pair[eq+1:], 64)
		if err != nil {
			return nil, fmt.Errorf("weight for %q: %v", name, err)
		}
		weights[name] = w
	}
	return weights, nil
}

type server struct {
	svc *fz.Service
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *server) handleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("GET only"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"explain": s.svc.Explain()})
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var query string
	switch r.Method {
	case http.MethodGet:
		query = r.URL.Query().Get("q")
	case http.MethodPost:
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		query = body.Query
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("GET or POST only"))
		return
	}
	res, err := s.svc.Search(query)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("POST only"))
		return
	}
	var dto []struct {
		Delete bool        `json:"delete"`
		Doc    fz.Document `json:"doc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	muts := make([]fz.Mutation, len(dto))
	for i, d := range dto {
		muts[i] = fz.Mutation{Delete: d.Delete, Doc: d.Doc}
	}
	if err := s.svc.Batch(muts); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "committed"})
}

func (s *server) handleDocument(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/documents/")
	id = strings.TrimSuffix(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown path"))
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		var doc struct {
			ID     string            `json:"id"`
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if doc.ID == "" {
			doc.ID = id
		}
		if doc.ID != id {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("document id %q conflicts with path %q", doc.ID, id))
			return
		}
		if err := s.svc.Upsert(fz.Document{ID: doc.ID, Fields: doc.Fields}); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "committed", "id": id})
	case http.MethodGet:
		doc, ok := s.svc.Get(id)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("document %q not found", id))
			return
		}
		writeJSON(w, http.StatusOK, doc)
	case http.MethodDelete:
		if err := s.svc.Delete(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("unsupported method"))
	}
}
