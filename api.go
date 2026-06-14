package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type ConfigHandler struct {
	store          *ConfigStore
	activity       *ActivityLog
	triggerRefresh func()
}

type ActivityHandler struct {
	activity *ActivityLog
}

func NewConfigHandler(store *ConfigStore, activity *ActivityLog, triggerRefresh func()) *ConfigHandler {
	return &ConfigHandler{store: store, activity: activity, triggerRefresh: triggerRefresh}
}

func NewActivityHandler(activity *ActivityLog) *ActivityHandler {
	return &ActivityHandler{activity: activity}
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "config store is unavailable")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/config":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, h.store.ConfigView())
	case path == "/api/config/server":
		if r.Method != http.MethodPut {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateServer(w, r)
	case path == "/api/config/hosts":
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, h.store.ConfigView()["hosts"])
			return
		}
		if r.Method == http.MethodPost {
			h.createHost(w, r)
			return
		}
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	case strings.HasPrefix(path, "/api/config/hosts/"):
		name, err := url.PathUnescape(strings.TrimPrefix(path, "/api/config/hosts/"))
		if err != nil || name == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid host name")
			return
		}
		if r.Method == http.MethodPut {
			h.updateHost(w, r, name)
			return
		}
		if r.Method == http.MethodDelete {
			h.deleteHost(w, r, name)
			return
		}
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

func (h *ConfigHandler) updateServer(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "config token is invalid")
		return
	}
	var input ServerConfigInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	view, err := h.store.UpdateServer(input)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.activity != nil {
		h.activity.Add("info", "config_update", "", "Server settings updated", nil)
	}
	h.trigger()
	writeJSON(w, http.StatusOK, view)
}

func (h *ConfigHandler) createHost(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "config token is invalid")
		return
	}
	var input HostConfigInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	view, _, err := h.store.UpsertHost("", input)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.activity != nil {
		h.activity.Add("info", "host_add", view.Name, "Host added: "+view.Name, map[string]string{"host": view.Host})
	}
	h.trigger()
	writeJSON(w, http.StatusCreated, view)
}

func (h *ConfigHandler) updateHost(w http.ResponseWriter, r *http.Request, name string) {
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "config token is invalid")
		return
	}
	var input HostConfigInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	view, _, err := h.store.UpsertHost(name, input)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.activity != nil {
		h.activity.Add("info", "host_update", view.Name, "Host updated: "+view.Name, map[string]string{"host": view.Host})
	}
	h.trigger()
	writeJSON(w, http.StatusOK, view)
}

func (h *ConfigHandler) deleteHost(w http.ResponseWriter, r *http.Request, name string) {
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "config token is invalid")
		return
	}
	view, err := h.store.DeleteHost(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if h.activity != nil {
		h.activity.Add("warn", "host_delete", view.Name, "Host deleted: "+view.Name, map[string]string{"host": view.Host})
	}
	h.trigger()
	writeJSON(w, http.StatusOK, view)
}

func (h *ConfigHandler) authorized(r *http.Request) bool {
	token := h.store.Terminal().Token
	if token == "" {
		return true
	}
	supplied := r.Header.Get("X-GPUBeat-Terminal-Token")
	if supplied == "" {
		supplied = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1
}

func (h *ConfigHandler) trigger() {
	if h.triggerRefresh != nil {
		h.triggerRefresh()
	}
}

func (h *ActivityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 200)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": h.activity.List(limit),
	})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func parseOptionalInt(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	return n, err == nil
}
