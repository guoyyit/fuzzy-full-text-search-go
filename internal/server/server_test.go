package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/service"
)

func newTestServer(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	svc, err := service.Open(t.TempDir(), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(svc).Routes())
	return ts, func() { ts.Close(); svc.Close() }
}

func TestHTTPLifecycle(t *testing.T) {
	ts, cleanup := newTestServer(t)
	defer cleanup()

	// 新增
	body := `{"id":"d1","fields":{"title":"hello world"}}`
	res, err := http.Post(ts.URL+"/documents", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("upsert status %d", res.StatusCode)
	}

	// 查询
	qres, err := http.Get(ts.URL + "/query?q=helo")
	if err != nil {
		t.Fatal(err)
	}
	defer qres.Body.Close()
	var out map[string]any
	json.NewDecoder(qres.Body).Decode(&out)
	hits := out["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("fuzzy query should find d1: %v", out)
	}

	// 删除
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/documents/d1", nil)
	dres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dres.Body.Close()

	qres2, err := http.Get(ts.URL + "/query?q=hello")
	if err != nil {
		t.Fatal(err)
	}
	defer qres2.Body.Close()
	if err := json.NewDecoder(qres2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out["hits"].([]any)) != 0 {
		t.Fatal("document still found after delete")
	}
}

func TestHTTPBadRequests(t *testing.T) {
	ts, cleanup := newTestServer(t)
	defer cleanup()

	cases := []struct{ method, url, body string }{
		{http.MethodGet, ts.URL + "/documents", ""},
		{http.MethodPost, ts.URL + "/documents", "not-json"},
		{http.MethodPost, ts.URL + "/documents", `{"id":"","fields":{}}`},
		{http.MethodGet, ts.URL + "/query", ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, c.url, bytes.NewBufferString(c.body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode < 400 {
			t.Errorf("%s %s expected 4xx, got %d", c.method, c.url, res.StatusCode)
		}
		res.Body.Close()
	}
}

func TestHTTPPutOverwrite(t *testing.T) {
	ts, cleanup := newTestServer(t)
	defer cleanup()
	http.Post(ts.URL+"/documents", "application/json", bytes.NewBufferString(`{"id":"d","fields":{"body":"old text"}}`))
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/documents/d", bytes.NewBufferString(`{"fields":{"body":"new text"}}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	out, err := http.Get(ts.URL + "/query?q=old")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	var r map[string]any
	if err := json.NewDecoder(out.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if len(r["hits"].([]any)) != 0 {
		t.Fatal("overwritten content must not match")
	}
}
