package main

import (
	"fmt"
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
		log.Printf("[连接池] %s 连接断开, 准备重连", addr)
	}
	p.mu.Unlock()

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
		log.Printf("[连接池] %s 创建连接失败: %v", addr, err)
		return nil, err
	}

	p.mu.Lock()
	p.clients[addr] = client
	p.mu.Unlock()

	log.Printf("[连接池] %s 创建连接成功", addr)
	return client, nil
}

func (p *SSHPool) ExecuteCommand(host string, port int, username, password, command string) (string, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	client, err := p.getClient(host, port, username, password)
	if err != nil {
		if strings.Contains(err.Error(), "unable to authenticate") {
			return "", fmt.Errorf("SSH认证失败: %w", err)
		}
		return "", fmt.Errorf("SSH连接失败: %w", err)
	}

	session, err := client.NewSession()
	if err != nil {
		log.Printf("[连接池] %s 会话创建失败, 清除连接并重连", addr)
		p.mu.Lock()
		if c, ok := p.clients[addr]; ok {
			c.Close()
			delete(p.clients, addr)
		}
		p.mu.Unlock()

		client, err = p.getClient(host, port, username, password)
		if err != nil {
			return "", fmt.Errorf("SSH重连失败: %w", err)
		}
		log.Printf("[连接池] %s 重连成功", addr)
		session, err = client.NewSession()
		if err != nil {
			return "", fmt.Errorf("创建SSH会话失败: %w", err)
		}
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		out := string(output)
		if strings.Contains(out, "command not found") || strings.Contains(out, "not found") {
			return "", fmt.Errorf("nvidia-smi未安装: %s", strings.TrimSpace(out))
		}
		if strings.Contains(out, "NVIDIA-SMI has failed") || strings.Contains(out, "Failed to initialize NVML") {
			return "", fmt.Errorf("nvidia-smi执行失败: %s", strings.TrimSpace(out))
		}
		return "", fmt.Errorf("命令执行失败: %s", strings.TrimSpace(out))
	}

	return string(output), nil
}

func (p *SSHPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, client := range p.clients {
		client.Close()
	}
	p.clients = make(map[string]*ssh.Client)
}
