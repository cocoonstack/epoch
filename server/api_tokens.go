package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) apiListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": plaintext, "name": req.Name})
}

func (s *Server) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositivePathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteToken(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
