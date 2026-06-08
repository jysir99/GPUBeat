package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type terminalOpener interface {
	OpenTerminal(hostCfg HostConfig, cols, rows int) (TerminalSession, error)
}

type TerminalHandler struct {
	cfg    *Config
	opener terminalOpener
}

type terminalClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type terminalServerMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
}

func NewTerminalHandler(cfg *Config, opener terminalOpener) *TerminalHandler {
	return &TerminalHandler{cfg: cfg, opener: opener}
}

func (h *TerminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil || !h.cfg.Server.Terminal.Enabled {
		http.Error(w, "terminal is disabled", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r) {
		http.Error(w, "terminal token is invalid", http.StatusUnauthorized)
		return
	}

	hostCfg, ok := h.findHost(r.URL.Query().Get("host"))
	if !ok {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	ws, err := acceptWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	session, err := h.opener.OpenTerminal(hostCfg, parsePositiveInt(r.URL.Query().Get("cols"), 100), parsePositiveInt(r.URL.Query().Get("rows"), 30))
	if err != nil {
		_ = ws.WriteJSON(terminalServerMessage{Type: "error", Data: err.Error()})
		return
	}
	defer session.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = ws.Close()
	}()

	var writeMu sync.Mutex
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = ws.WriteJSON(terminalServerMessage{Type: "output", Data: string(buf[:n])})
				writeMu.Unlock()
			}
			if err != nil {
				writeMu.Lock()
				_ = ws.WriteJSON(terminalServerMessage{Type: "close", Data: "terminal session ended"})
				writeMu.Unlock()
				cancel()
				return
			}
		}
	}()

	go func() {
		_ = session.Wait()
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
			return
		}
	}
}

func (h *TerminalHandler) authorized(r *http.Request) bool {
	token := h.cfg.Server.Terminal.Token
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
	for _, host := range h.cfg.Hosts {
		if host.Name == name {
			return host, true
		}
	}
	return HostConfig{}, false
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
