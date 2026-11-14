// Package api is the HTTP interface: create problems, submit code, poll for
// results.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"codejudge/internal/judge"
	"codejudge/internal/store"
)

type Server struct {
	store *store.Store
}

func New(st *store.Store) *Server {
	return &Server{store: st}
}

// Routes uses the "METHOD /path/{param}" patterns that net/http has supported
// since Go 1.22, so no router library is needed.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /problems", s.createProblem)
	mux.HandleFunc("POST /submissions", s.createSubmission)
	mux.HandleFunc("GET /submissions/{id}", s.getSubmission)
	return mux
}

func (s *Server) createProblem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title         string `json:"title"`
		TimeLimitMs   int    `json:"time_limit_ms"`
		MemoryLimitMb int    `json:"memory_limit_mb"`
		TestCases     []struct {
			Input          string `json:"input"`
			ExpectedOutput string `json:"expected_output"`
		} `json:"test_cases"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title == "" || len(req.TestCases) == 0 {
		writeError(w, http.StatusBadRequest, "title and at least one test case are required")
		return
	}
	if req.TimeLimitMs <= 0 {
		req.TimeLimitMs = 2000
	}
	if req.MemoryLimitMb <= 0 {
		req.MemoryLimitMb = 256
	}

	tests := make([]store.TestCase, len(req.TestCases))
	for i, tc := range req.TestCases {
		tests[i] = store.TestCase{Input: tc.Input, ExpectedOutput: tc.ExpectedOutput}
	}

	id, err := s.store.CreateProblem(r.Context(), req.Title, req.TimeLimitMs, req.MemoryLimitMb, tests)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"problem_id": id})
}

func (s *Server) createSubmission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProblemID  string `json:"problem_id"`
		Language   string `json:"language"`
		SourceCode string `json:"source_code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !judge.Supported(req.Language) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("language %q is not supported", req.Language))
		return
	}
	if req.SourceCode == "" {
		writeError(w, http.StatusBadRequest, "source_code is empty")
		return
	}

	id, err := s.store.CreateSubmission(r.Context(), req.ProblemID, req.Language, req.SourceCode)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "problem not found")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	// 202 Accepted rather than 201 Created: we've queued the work, but it
	// hasn't been done yet. The client polls GET /submissions/{id} for the
	// result.
	writeJSON(w, http.StatusAccepted, map[string]string{"submission_id": id})
}

func (s *Server) getSubmission(w http.ResponseWriter, r *http.Request) {
	sub, err := s.store.GetSubmission(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	type testJSON struct {
		Test      int    `json:"test"`
		Verdict   string `json:"verdict"`
		RuntimeMs int    `json:"runtime_ms"`
	}
	tests := make([]testJSON, len(sub.Tests)) // never nil, so it's [] in JSON, not null
	for i, t := range sub.Tests {
		tests[i] = testJSON{t.Number, t.Verdict, t.RuntimeMs}
	}
	writeJSON(w, http.StatusOK, struct {
		Status    string     `json:"status"`
		Verdict   *string    `json:"verdict"`
		RuntimeMs *int       `json:"runtime_ms"`
		Tests     []testJSON `json:"tests"`
	}{sub.Status, sub.Verdict, sub.RuntimeMs, tests})
}

// decodeJSON reads the request body into dst and writes a 400 if it can't.
// It refuses bodies over 1 MB, so a client can't make us read unlimited data,
// and unknown fields, so a typo like "sorce_code" is an error instead of
// silently becoming an empty value.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalError logs the real error for us and gives the client a generic
// message, so database details don't leak out of the API.
func internalError(w http.ResponseWriter, err error) {
	log.Printf("api: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
