package main

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func ExecuteCommand(host string, port int, username, password, command string) (string, error) {
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		if strings.Contains(err.Error(), "unable to authenticate") {
			return "", fmt.Errorf("SSH认证失败: %w", err)
		}
		return "", fmt.Errorf("SSH连接失败: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建SSH会话失败: %w", err)
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
