package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

type Cache struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

func NewCache() *Cache {
	return &Cache{
		data: make(map[string]interface{}),
	}
}

func (c *Cache) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

var cache = NewCache()

func handleIndex(w http.ResponseWriter, r *http.Request) {
	data, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func serveWebAsset(assetPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := webFS.ReadFile(assetPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, assetPath, time.Time{}, bytes.NewReader(data))
	}
}

func handleGPUStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data, ok := cache.Get("gpustats")
	if !ok {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"gpustats":    []interface{}{},
			"update_time": "",
		})
		return
	}
	json.NewEncoder(w).Encode(data)
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack is not supported")
	}
	return hijacker.Hijack()
}

func loggingMiddleware(next http.HandlerFunc, accessLogger *Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next(rw, r)
		duration := time.Since(start)
		log.Printf("[HTTP] %s %s %s %d %v", r.Method, r.URL.Path, r.RemoteAddr, rw.status, duration)
		if accessLogger != nil {
			accessLogger.LogAccess(r.Method, r.URL.Path, r.RemoteAddr, rw.status, duration)
		}
	}
}
