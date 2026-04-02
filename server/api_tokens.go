package server

import (
	"encoding/json"
	"net/http"
)

// GET /api/tokens
func (s *Server) apiListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, tokens)
}

// POST /api/tokens — body: {"name":"my-token"}
func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	createdBy := ""
	if s.sso != nil {
		if sess := s.getSession(r); sess != nil {
			createdBy = sess.User
		}
	}
	plaintext, err := s.store.CreateToken(r.Context(), req.Name, createdBy)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"token": plaintext, "name": req.Name})
}

// DELETE /api/tokens/{id}
func (s *Server) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositivePathID(r, "id")
	if err != nil {
		writeError(w, 400, "invalid id")
		return
	}
	if err := s.store.DeleteToken(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
