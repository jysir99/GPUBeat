package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTerminalOpener struct {
	mu      sync.Mutex
	session *fakeTerminalSession
	host    HostConfig
	cols    int
	rows    int
	calls   int
}

func (o *fakeTerminalOpener) OpenTerminal(hostCfg HostConfig, cols, rows int) (TerminalSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.host = hostCfg
	o.cols = cols
	o.rows = rows
	if o.session == nil {
		o.session = newFakeTerminalSession()
	}
	return o.session, nil
}

type resizeCall struct {
	cols int
	rows int
}

type fakeTerminalSession struct {
	reader  *io.PipeReader
	writer  *io.PipeWriter
	inputs  chan string
	resizes chan resizeCall
	done    chan struct{}
	once    sync.Once
}

func newFakeTerminalSession() *fakeTerminalSession {
	r, w := io.Pipe()
	return &fakeTerminalSession{
		reader:  r,
		writer:  w,
		inputs:  make(chan string, 4),
		resizes: make(chan resizeCall, 4),
		done:    make(chan struct{}),
	}
}

func (s *fakeTerminalSession) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *fakeTerminalSession) Write(p []byte) (int, error) {
	s.inputs <- string(p)
	return len(p), nil
}

func (s *fakeTerminalSession) Resize(cols, rows int) error {
	s.resizes <- resizeCall{cols: cols, rows: rows}
	return nil
}

func (s *fakeTerminalSession) Close() error {
	s.once.Do(func() {
		_ = s.reader.Close()
		_ = s.writer.Close()
		close(s.done)
	})
	return nil
}

func (s *fakeTerminalSession) Wait() error {
	<-s.done
	return nil
}

func (s *fakeTerminalSession) send(text string) {
	_, _ = s.writer.Write([]byte(text))
}

func TestTerminalRejectsDisabledAndUnknownHosts(t *testing.T) {
	cfg := terminalTestConfig(false)
	opener := &fakeTerminalOpener{}
	handler := NewTerminalHandler(NewConfigStore("", cfg), opener, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/terminal?host=gpu-1", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("disabled terminal status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if opener.calls != 0 {
		t.Fatalf("opener called while terminal disabled")
	}

	cfg.Server.Terminal.Enabled = true
	handler = NewTerminalHandler(NewConfigStore("", cfg), opener, nil)
	req = httptest.NewRequest(http.MethodGet, "/api/terminal?host=missing", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown host status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if opener.calls != 0 {
		t.Fatalf("opener called for unknown host")
	}
}

func TestTerminalRequiresTokenWhenConfigured(t *testing.T) {
	cfg := terminalTestConfig(true)
	cfg.Server.Terminal.Token = "let-me-in"
	opener := &fakeTerminalOpener{}
	handler := NewTerminalHandler(NewConfigStore("", cfg), opener, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/terminal?host=gpu-1", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/terminal?host=gpu-1&token=wrong", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	if opener.calls != 0 {
		t.Fatalf("opener called before token authorization")
	}
}

func TestTerminalWebSocketForwardsOutputInputAndResizeThroughMiddleware(t *testing.T) {
	cfg := terminalTestConfig(true)
	session := newFakeTerminalSession()
	opener := &fakeTerminalOpener{session: session}
	handler := NewTerminalHandler(NewConfigStore("", cfg), opener, nil)
	server := httptest.NewServer(loggingMiddleware(handler.ServeHTTP, nil))
	defer server.Close()
	defer session.Close()

	ws, conn, cleanup := dialWebSocket(t, server.URL, "/api/terminal?host=gpu-1&cols=120&rows=40")
	defer cleanup()

	waitForOpenerCalls(t, opener, 1)
	opener.assertLastOpen(t, "gpu-1", 120, 40)

	go session.send("hello from server")
	var out terminalServerMessage
	readServerJSON(t, ws, &out)
	if out.Type != "output" || out.Data != "hello from server" {
		t.Fatalf("server message = %#v", out)
	}

	writeClientJSON(t, conn, terminalClientMessage{Type: "input", Data: "ls\r"})
	select {
	case input := <-session.inputs:
		if input != "ls\r" {
			t.Fatalf("input = %q, want ls\\r", input)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal input")
	}

	writeClientJSON(t, conn, terminalClientMessage{Type: "resize", Cols: 88, Rows: 24})
	select {
	case resize := <-session.resizes:
		if resize.cols != 88 || resize.rows != 24 {
			t.Fatalf("resize = %#v, want 88x24", resize)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal resize")
	}

	writeClientJSON(t, conn, terminalClientMessage{Type: "close"})
}

func TestTerminalWebSocketAcceptsConfiguredToken(t *testing.T) {
	cfg := terminalTestConfig(true)
	cfg.Server.Terminal.Token = "let-me-in"
	session := newFakeTerminalSession()
	opener := &fakeTerminalOpener{session: session}
	handler := NewTerminalHandler(NewConfigStore("", cfg), opener, nil)
	server := httptest.NewServer(loggingMiddleware(handler.ServeHTTP, nil))
	defer server.Close()
	defer session.Close()

	_, _, cleanup := dialWebSocket(t, server.URL, "/api/terminal?host=gpu-1&token=let-me-in")
	defer cleanup()

	waitForOpenerCalls(t, opener, 1)
}

func waitForOpenerCalls(t *testing.T, opener *fakeTerminalOpener, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		opener.mu.Lock()
		calls := opener.calls
		opener.mu.Unlock()
		if calls == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	opener.mu.Lock()
	defer opener.mu.Unlock()
	t.Fatalf("opener calls = %d, want %d", opener.calls, want)
}

func (o *fakeTerminalOpener) assertLastOpen(t *testing.T, host string, cols, rows int) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.host.Name != host || o.cols != cols || o.rows != rows {
		t.Fatalf("open args = host %q cols %d rows %d", o.host.Name, o.cols, o.rows)
	}
}

func TestWebSocketAcceptKey(t *testing.T) {
	got := websocketAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("accept key = %q, want %q", got, want)
	}
}

func terminalTestConfig(enabled bool) *Config {
	return &Config{
		Server: ServerConfig{Terminal: TerminalConfig{Enabled: enabled}},
		Hosts: []HostConfig{{
			Name:     "gpu-1",
			Host:     "192.0.2.10",
			Port:     22,
			Username: "root",
			Password: "secret",
		}},
	}
}

func dialWebSocket(t *testing.T, serverURL, path string) (*webSocketConn, net.Conn, func()) {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}

	key := "dGhlIHNhbXBsZSBub25jZQ=="
	request := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		t.Fatalf("websocket status = %q, want 101", strings.TrimSpace(status))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	ws := &webSocketConn{conn: conn, r: reader}
	return ws, conn, func() { _ = ws.Close() }
}

func writeClientJSON(t *testing.T, conn net.Conn, msg terminalClientMessage) {
	t.Helper()
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := websocketClientFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func readServerJSON(t *testing.T, ws *webSocketConn, v interface{}) {
	t.Helper()
	if err := ws.ReadJSON(v); err != nil {
		t.Fatal(err)
	}
}

func websocketClientFrame(payload []byte) ([]byte, error) {
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return nil, err
	}
	header := []byte{0x81}
	length := len(payload)
	if length < 126 {
		header = append(header, 0x80|byte(length))
	} else if length <= 0xffff {
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	} else {
		header = append(header, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(length))
		header = append(header, b[:]...)
	}
	header = append(header, mask...)
	out := append(header, make([]byte, length)...)
	for i := range payload {
		out[len(header)+i] = payload[i] ^ mask[i%4]
	}
	return out, nil
}
