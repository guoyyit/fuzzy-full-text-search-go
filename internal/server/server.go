// Package server 暴露全文检索服务的本地 HTTP/JSON 接口（无外部依赖）。
package server

import (
	"encoding/json"
	"net/http"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/service"
)

// Server 包装 service 提供 REST 接口。
type Server struct {
	svc *service.Service
}

func New(svc *service.Service) *Server { return &Server{svc: svc} }

// Routes 注册路由。
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/documents", s.handleDocuments)
	mux.HandleFunc("/documents/", s.handleDocumentByID)
	mux.HandleFunc("/query", s.handleQuery)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /documents  body: {"id":"...","fields":{"k":"v"}}
func (s *Server) handleDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var d model.Document
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := s.svc.Upsert(d); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "committed", "id": d.ID})
}

// PUT /documents/{id} 覆盖更新；DELETE /documents/{id} 删除。
func (s *Server) handleDocumentByID(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/documents/"):]
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少文档 ID")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var d model.Document
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
			return
		}
		d.ID = id // 以路径 ID 为准
		if err := s.svc.Upsert(d); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "committed", "id": id})
	case http.MethodDelete:
		if err := s.svc.Delete(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 PUT 或 DELETE")
	}
}

// queryRequest 同时支持 GET /query?q=... 与 POST /query body {"query":"..."}。
type queryRequest struct {
	Query string `json:"query"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var q string
	switch r.Method {
	case http.MethodGet:
		q = r.URL.Query().Get("q")
	case http.MethodPost:
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
			return
		}
		q = req.Query
	default:
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET 或 POST")
		return
	}
	if q == "" {
		writeErr(w, http.StatusBadRequest, "查询参数 q/query 不能为空")
		return
	}
	res, err := s.svc.Query(q)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
