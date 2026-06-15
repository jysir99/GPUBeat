package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type terminalOpener interface {
	OpenTerminal(hostCfg HostConfig, cols, rows int) (TerminalSession, error)
}

type TerminalHandler struct {
	store    *ConfigStore
	opener   terminalOpener
	activity *ActivityLog
	manager  *TerminalManager
}

type terminalClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type terminalServerMessage struct {
	Type      string `json:"type"`
	Data      string `json:"data,omitempty"`
	SessionID string `json:"session,omitempty"`
}

type terminalSessionInfo struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Attached  int    `json:"attached"`
}

func NewTerminalHandler(store *ConfigStore, opener terminalOpener, activity *ActivityLog) *TerminalHandler {
	return &TerminalHandler{
		store:    store,
		opener:   opener,
		activity: activity,
		manager:  NewTerminalManager(opener, activity),
	}
}

func (h *TerminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || !h.store.Terminal().Enabled {
		writeJSONError(w, http.StatusForbidden, "terminal is disabled")
		return
	}
	if !h.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "terminal token is invalid")
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/terminal":
		h.serveWebSocket(w, r)
	case path == "/api/terminal/sessions":
		h.serveSessionList(w, r)
	case strings.HasPrefix(path, "/api/terminal/sessions/"):
		sessionID, err := url.PathUnescape(strings.TrimPrefix(path, "/api/terminal/sessions/"))
		if err != nil || sessionID == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid terminal session id")
			return
		}
		h.serveSession(w, r, sessionID)
	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

func (h *TerminalHandler) Close() {
	if h.manager != nil {
		h.manager.CloseAll()
	}
}

func (h *TerminalHandler) serveSessionList(w http.ResponseWriter, r *http.Request) {
	hostCfg, ok := h.findHost(r.URL.Query().Get("host"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown host")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"sessions": h.manager.List(hostCfg.Name),
		})
	case http.MethodPost:
		session, err := h.manager.Create(hostCfg, parsePositiveInt(r.URL.Query().Get("cols"), 100), parsePositiveInt(r.URL.Query().Get("rows"), 30))
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, session.Info())
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *TerminalHandler) serveSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	switch r.Method {
	case http.MethodDelete:
		info, ok := h.manager.Close(sessionID, "terminal session closed")
		if !ok {
			writeJSONError(w, http.StatusNotFound, "terminal session not found")
			return
		}
		writeJSON(w, http.StatusOK, info)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *TerminalHandler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hostCfg, ok := h.findHost(r.URL.Query().Get("host"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown host")
		return
	}

	ws, err := acceptWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	cols := parsePositiveInt(r.URL.Query().Get("cols"), 100)
	rows := parsePositiveInt(r.URL.Query().Get("rows"), 30)
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	ephemeral := sessionID == ""
	var session *managedTerminalSession
	if ephemeral {
		session, err = h.manager.Create(hostCfg, cols, rows)
		if err != nil {
			_ = ws.WriteJSON(terminalServerMessage{Type: "error", Data: err.Error()})
			return
		}
		defer h.manager.Close(session.ID(), "terminal session closed")
	} else {
		var ok bool
		session, ok = h.manager.Get(sessionID)
		if !ok || session.Host() != hostCfg.Name {
			_ = ws.WriteJSON(terminalServerMessage{Type: "error", Data: "terminal session not found"})
			return
		}
		_ = session.Resize(cols, rows)
	}

	client, replay, ok := session.Attach()
	if !ok {
		_ = ws.WriteJSON(terminalServerMessage{Type: "error", Data: "terminal session is closed", SessionID: session.ID()})
		return
	}
	defer session.Detach(client)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = ws.Close()
	}()

	go func() {
		if replay != "" {
			_ = ws.WriteJSON(terminalServerMessage{Type: "output", Data: replay, SessionID: session.ID()})
		}
		for msg := range client {
			_ = ws.WriteJSON(msg)
		}
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var msg terminalClientMessage
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "input":
			if msg.Data != "" {
				_, _ = session.Write([]byte(msg.Data))
			}
		case "resize":
			_ = session.Resize(msg.Cols, msg.Rows)
		case "close":
			_, _ = h.manager.Close(session.ID(), "terminal session closed")
			return
		case "detach":
			return
		}
	}
}

func (h *TerminalHandler) authorized(r *http.Request) bool {
	token := h.store.Terminal().Token
	if token == "" {
		return true
	}
	supplied := r.URL.Query().Get("token")
	if supplied == "" {
		supplied = r.Header.Get("X-GPUBeat-Terminal-Token")
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1
}

func (h *TerminalHandler) findHost(name string) (HostConfig, bool) {
	if name == "" {
		return HostConfig{}, false
	}
	return h.store.FindHost(name)
}

const terminalReplayLimit = 256 * 1024

type TerminalManager struct {
	mu       sync.RWMutex
	opener   terminalOpener
	activity *ActivityLog
	sessions map[string]*managedTerminalSession
	byHost   map[string][]string
	nextID   uint64
	nextSeq  map[string]int
}

func NewTerminalManager(opener terminalOpener, activity *ActivityLog) *TerminalManager {
	return &TerminalManager{
		opener:   opener,
		activity: activity,
		sessions: make(map[string]*managedTerminalSession),
		byHost:   make(map[string][]string),
		nextSeq:  make(map[string]int),
	}
}

func (m *TerminalManager) Create(hostCfg HostConfig, cols, rows int) (*managedTerminalSession, error) {
	session, err := m.opener.OpenTerminal(hostCfg, cols, rows)
	if err != nil {
		if m.activity != nil {
			m.activity.Add("error", "terminal_error", hostCfg.Name, "Terminal failed: "+err.Error(), map[string]string{"host": hostCfg.Host})
		}
		return nil, err
	}

	m.mu.Lock()
	m.nextID++
	m.nextSeq[hostCfg.Name]++
	id := fmt.Sprintf("term-%d-%d", time.Now().UnixNano(), m.nextID)
	title := fmt.Sprintf("Terminal %d", m.nextSeq[hostCfg.Name])
	managed := &managedTerminalSession{
		id:        id,
		host:      hostCfg.Name,
		hostAddr:  hostCfg.Host,
		title:     title,
		session:   session,
		createdAt: time.Now(),
		updatedAt: time.Now(),
		clients:   make(map[chan terminalServerMessage]struct{}),
		manager:   m,
	}
	m.sessions[id] = managed
	m.byHost[hostCfg.Name] = append(m.byHost[hostCfg.Name], id)
	m.mu.Unlock()

	if m.activity != nil {
		m.activity.Add("info", "terminal_open", hostCfg.Name, title+" opened for "+hostCfg.Name, map[string]string{
			"host":    hostCfg.Host,
			"session": id,
			"title":   title,
		})
	}
	managed.Start()
	return managed, nil
}

func (m *TerminalManager) List(host string) []terminalSessionInfo {
	m.mu.RLock()
	ids := append([]string(nil), m.byHost[host]...)
	sessions := make([]*managedTerminalSession, 0, len(ids))
	for _, id := range ids {
		if session, ok := m.sessions[id]; ok {
			sessions = append(sessions, session)
		}
	}
	m.mu.RUnlock()

	out := make([]terminalSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.Info())
	}
	return out
}

func (m *TerminalManager) Get(id string) (*managedTerminalSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[id]
	return session, ok
}

func (m *TerminalManager) Close(id, reason string) (terminalSessionInfo, bool) {
	session, ok := m.Get(id)
	if !ok {
		return terminalSessionInfo{}, false
	}
	info := session.Info()
	session.Close(reason)
	return info, true
}

func (m *TerminalManager) CloseAll() {
	m.mu.RLock()
	sessions := make([]*managedTerminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	for _, session := range sessions {
		session.Close("terminal session closed")
	}
}

func (m *TerminalManager) remove(id string) {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		ids := m.byHost[session.Host()]
		for i, existing := range ids {
			if existing == id {
				m.byHost[session.Host()] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		if len(m.byHost[session.Host()]) == 0 {
			delete(m.byHost, session.Host())
		}
	}
	m.mu.Unlock()
}

type managedTerminalSession struct {
	mu        sync.Mutex
	id        string
	host      string
	hostAddr  string
	title     string
	session   TerminalSession
	createdAt time.Time
	updatedAt time.Time
	buffer    []byte
	clients   map[chan terminalServerMessage]struct{}
	closed    bool
	closeOnce sync.Once
	manager   *TerminalManager
}

func (s *managedTerminalSession) ID() string {
	return s.id
}

func (s *managedTerminalSession) Host() string {
	return s.host
}

func (s *managedTerminalSession) Start() {
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := s.session.Read(buf)
			if n > 0 {
				s.broadcast(terminalServerMessage{Type: "output", Data: string(buf[:n]), SessionID: s.id})
			}
			if err != nil {
				s.finish("terminal session ended")
				return
			}
		}
	}()

	go func() {
		_ = s.session.Wait()
		s.finish("terminal session ended")
	}()
}

func (s *managedTerminalSession) Info() terminalSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return terminalSessionInfo{
		ID:        s.id,
		Host:      s.host,
		Title:     s.title,
		CreatedAt: s.createdAt.Format(time.RFC3339),
		UpdatedAt: s.updatedAt.Format(time.RFC3339),
		Attached:  len(s.clients),
	}
}

func (s *managedTerminalSession) Attach() (chan terminalServerMessage, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, "", false
	}
	client := make(chan terminalServerMessage, 256)
	s.clients[client] = struct{}{}
	s.updatedAt = time.Now()
	return client, string(s.buffer), true
}

func (s *managedTerminalSession) Detach(client chan terminalServerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[client]; ok {
		delete(s.clients, client)
		close(client)
		s.updatedAt = time.Now()
	}
}

func (s *managedTerminalSession) Write(p []byte) (int, error) {
	return s.session.Write(p)
}

func (s *managedTerminalSession) Resize(cols, rows int) error {
	return s.session.Resize(cols, rows)
}

func (s *managedTerminalSession) Close(reason string) {
	s.finish(reason)
}

func (s *managedTerminalSession) broadcast(msg terminalServerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if msg.Type == "output" && msg.Data != "" {
		s.buffer = append(s.buffer, []byte(msg.Data)...)
		if len(s.buffer) > terminalReplayLimit {
			s.buffer = append([]byte(nil), s.buffer[len(s.buffer)-terminalReplayLimit:]...)
		}
	}
	s.updatedAt = time.Now()
	for client := range s.clients {
		select {
		case client <- msg:
		default:
		}
	}
}

func (s *managedTerminalSession) finish(reason string) {
	s.closeOnce.Do(func() {
		_ = s.session.Close()

		s.mu.Lock()
		s.closed = true
		s.updatedAt = time.Now()
		clients := s.clients
		s.clients = make(map[chan terminalServerMessage]struct{})
		msg := terminalServerMessage{Type: "close", Data: reason, SessionID: s.id}
		for client := range clients {
			select {
			case client <- msg:
			default:
			}
			close(client)
		}
		s.mu.Unlock()

		if s.manager != nil {
			if s.manager.activity != nil {
				s.manager.activity.Add("info", "terminal_close", s.host, s.title+" closed for "+s.host, map[string]string{
					"host":    s.hostAddr,
					"session": s.id,
					"title":   s.title,
				})
			}
			s.manager.remove(s.id)
		}
	})
}

func parsePositiveInt(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

type webSocketConn struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
}

func acceptWebSocket(w http.ResponseWriter, r *http.Request) (*webSocketConn, error) {
	if !headerContainsToken(r.Header.Get("Upgrade"), "websocket") || !headerContainsToken(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("not a websocket upgrade")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported websocket version")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("websocket hijack is not supported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}

	accept := websocketAcceptKey(key)
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(response)); err != nil {
		conn.Close()
		return nil, err
	}

	return &webSocketConn{conn: conn, r: rw.Reader}, nil
}

func headerContainsToken(value, token string) bool {
	token = strings.ToLower(token)
	for _, part := range strings.Split(value, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == token {
			return true
		}
	}
	return false
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (c *webSocketConn) ReadJSON(v interface{}) error {
	typ, payload, err := c.readFrame()
	if err != nil {
		return err
	}
	if typ == 8 {
		return io.EOF
	}
	if typ != 1 {
		return fmt.Errorf("unsupported websocket frame type %d", typ)
	}
	return json.Unmarshal(payload, v)
}

func (c *webSocketConn) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(1, data)
}

func (c *webSocketConn) Close() error {
	_ = c.writeFrame(8, []byte{})
	return c.conn.Close()
}

func (c *webSocketConn) readFrame() (byte, []byte, error) {
	first, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	second, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}

	opcode := first & 0x0f
	masked := second&0x80 != 0
	length := uint64(second & 0x7f)
	if length == 126 {
		var b [2]byte
		if _, err := io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	} else if length == 127 {
		var b [8]byte
		if _, err := io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	if length > 1<<20 {
		return 0, nil, errors.New("websocket frame too large")
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func (c *webSocketConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	header := []byte{0x80 | opcode}
	length := len(payload)
	if length < 126 {
		header = append(header, byte(length))
	} else if length <= 0xffff {
		header = append(header, 126, byte(length>>8), byte(length))
	} else {
		header = append(header, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(length))
		header = append(header, b[:]...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}
