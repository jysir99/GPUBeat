package main

import (
	"embed"
	"encoding/json"
	"net/http"
	"sync"
)

//go:embed web/index.html
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
