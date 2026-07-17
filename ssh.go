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

// sshError 携带语言中立的错误码 code 与完整中文错误 msg。
// code 输出到前端供 i18n 翻译,msg 仅供日志。
type sshError struct {
	code string
	msg  string
}

func (e *sshError) Error() string { return e.msg }

func newSSHError(code, msg string) *sshError {
	return &sshError{code: code, msg: msg}
}

// errorCode 从 err 中提取错误码,无法识别时返回 "unknown"。
func errorCode(err error) string {
	if e, ok := err.(*sshError); ok && e.code != "" {
		return e.code
	}
	return "unknown"
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
			return "", newSSHError("ssh.auth", fmt.Sprintf("SSH认证失败: %v", err))
		}
		return "", newSSHError("ssh.connect", fmt.Sprintf("SSH连接失败: %v", err))
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
			return "", newSSHError("ssh.reconnect", fmt.Sprintf("SSH重连失败: %v", err))
		}
		log.Printf("[连接池] %s 重连成功", addr)
		session, err = client.NewSession()
		if err != nil {
			return "", newSSHError("ssh.session", fmt.Sprintf("创建SSH会话失败: %v", err))
		}
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		out := string(output)
		if strings.Contains(out, "command not found") || strings.Contains(out, "not found") {
			return "", newSSHError("nvsmi.notfound", fmt.Sprintf("nvidia-smi未安装: %s", strings.TrimSpace(out)))
		}
		if strings.Contains(out, "NVIDIA-SMI has failed") || strings.Contains(out, "Failed to initialize NVML") {
			return "", newSSHError("nvsmi.failed", fmt.Sprintf("nvidia-smi执行失败: %s", strings.TrimSpace(out)))
		}
		return "", newSSHError("cmd.failed", fmt.Sprintf("命令执行失败: %s", strings.TrimSpace(out)))
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
