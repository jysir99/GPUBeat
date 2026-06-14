package main

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHPool struct {
	mu      sync.Mutex
	clients map[string]*ssh.Client
}

func NewSSHPool() *SSHPool {
	return &SSHPool{
		clients: make(map[string]*ssh.Client),
	}
}

func dialSSHClient(host string, port int, username, password string) (*ssh.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		log.Printf("[SSH] %s dial failed: %v", addr, err)
		return nil, err
	}

	return client, nil
}

func (p *SSHPool) getClient(host string, port int, username, password string) (*ssh.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	p.mu.Lock()
	if client, ok := p.clients[addr]; ok {
		session, err := client.NewSession()
		if err == nil {
			session.Close()
			p.mu.Unlock()
			return client, nil
		}
		client.Close()
		delete(p.clients, addr)
		log.Printf("[SSH] %s connection dropped, reconnecting", addr)
	}
	p.mu.Unlock()

	client, err := dialSSHClient(host, port, username, password)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.clients[addr] = client
	p.mu.Unlock()

	log.Printf("[SSH] %s connected", addr)
	return client, nil
}

func (p *SSHPool) ExecuteCommand(host string, port int, username, password, command string) (string, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	client, err := p.getClient(host, port, username, password)
	if err != nil {
		if strings.Contains(err.Error(), "unable to authenticate") {
			return "", fmt.Errorf("SSH authentication failed: %w", err)
		}
		return "", fmt.Errorf("SSH connection failed: %w", err)
	}

	session, err := client.NewSession()
	if err != nil {
		log.Printf("[SSH] %s session failed, reconnecting", addr)
		p.mu.Lock()
		if c, ok := p.clients[addr]; ok {
			c.Close()
			delete(p.clients, addr)
		}
		p.mu.Unlock()

		client, err = p.getClient(host, port, username, password)
		if err != nil {
			return "", fmt.Errorf("SSH reconnect failed: %w", err)
		}
		log.Printf("[SSH] %s reconnected", addr)
		session, err = client.NewSession()
		if err != nil {
			return "", fmt.Errorf("create SSH session failed: %w", err)
		}
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		out := string(output)
		return "", fmt.Errorf("command failed: %s", strings.TrimSpace(out))
	}

	return string(output), nil
}

type TerminalSession interface {
	io.Reader
	io.Writer
	Resize(cols, rows int) error
	Close() error
	Wait() error
}

type sshTerminalSession struct {
	session *ssh.Session
	client  *ssh.Client
	stdin   io.WriteCloser
	output  *io.PipeReader
}

func (s *sshTerminalSession) Read(p []byte) (int, error) {
	return s.output.Read(p)
}

func (s *sshTerminalSession) Write(p []byte) (int, error) {
	return s.stdin.Write(p)
}

func (s *sshTerminalSession) Resize(cols, rows int) error {
	if cols <= 0 {
		cols = 100
	}
	if rows <= 0 {
		rows = 30
	}
	return s.session.WindowChange(rows, cols)
}

func (s *sshTerminalSession) Wait() error {
	return s.session.Wait()
}

func (s *sshTerminalSession) Close() error {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.output != nil {
		_ = s.output.Close()
	}
	err := s.session.Close()
	if s.client != nil {
		_ = s.client.Close()
	}
	return err
}

func (p *SSHPool) OpenTerminal(hostCfg HostConfig, cols, rows int) (TerminalSession, error) {
	if cols <= 0 {
		cols = 100
	}
	if rows <= 0 {
		rows = 30
	}

	client, err := dialSSHClient(hostCfg.Host, hostCfg.Port, hostCfg.Username, hostCfg.Password)
	if err != nil {
		return nil, fmt.Errorf("SSH terminal connection failed: %w", err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		client, err = dialSSHClient(hostCfg.Host, hostCfg.Port, hostCfg.Username, hostCfg.Password)
		if err != nil {
			return nil, fmt.Errorf("SSH terminal reconnect failed: %w", err)
		}
		session, err = client.NewSession()
		if err != nil {
			return nil, fmt.Errorf("SSH terminal session failed: %w", err)
		}
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("terminal stdin failed: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("terminal stdout failed: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("terminal stderr failed: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		session.Close()
		return nil, fmt.Errorf("request pty failed: %w", err)
	}

	outputReader, outputWriter := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(outputWriter, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(outputWriter, stderr)
	}()
	go func() {
		wg.Wait()
		_ = outputWriter.Close()
	}()

	if err := session.Shell(); err != nil {
		session.Close()
		_ = outputReader.Close()
		return nil, fmt.Errorf("start shell failed: %w", err)
	}

	return &sshTerminalSession{
		session: session,
		client:  client,
		stdin:   stdin,
		output:  outputReader,
	}, nil
}

func (p *SSHPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, client := range p.clients {
		client.Close()
	}
	p.clients = make(map[string]*ssh.Client)
}
