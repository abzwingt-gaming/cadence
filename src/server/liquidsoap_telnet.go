// liquidsoap_telnet.go — telnet protocol client for Liquidsoap.
// Used when CSERVER_LIQUIDSOAP_MODE != "http" (default).

package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

func liquidsoapTelnet(cmd string) error {
	conf := c()
	addr := conf.LiquidsoapAddress + ":" + conf.LiquidsoapPort
	conn, err := net.DialTimeout("tcp", addr, conf.LiquidsoapTimeout)
	if err != nil {
		return fmt.Errorf("liquidsoap telnet connect %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(conf.LiquidsoapTimeout))

	_, err = fmt.Fprintf(conn, "%s\n", strings.TrimSpace(cmd))
	if err != nil {
		return fmt.Errorf("liquidsoap telnet write: %w", err)
	}

	// Read response until "END" sentinel or EOF.
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			break
		}
		slog.Debug("Liquidsoap telnet response.", "line", line)
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("Liquidsoap telnet read error.", "cmd", cmd, "error", err)
	}
	return nil
}
