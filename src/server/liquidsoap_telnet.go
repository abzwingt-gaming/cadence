// liquidsoap_telnet.go — telnet protocol client for Liquidsoap.
// Used when CSERVER_LIQUIDSOAP_MODE != "http".

package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

// liquidsoapTelnet opens a TCP connection to Liquidsoap's telnet port,
// sends cmd, and reads the response until the "END" sentinel line.
// Returns an error if the connection fails, the write fails, or the
// server closes without sending "END".
func liquidsoapTelnet(cmd string) error {
	conf := c()
	addr := net.JoinHostPort(conf.LiquidsoapAddress, conf.LiquidsoapPort)

	conn, err := net.DialTimeout("tcp", addr, conf.LiquidsoapTimeout)
	if err != nil {
		return fmt.Errorf("liquidsoap telnet connect %s: %w", addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(conf.LiquidsoapTimeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("liquidsoap telnet set deadline: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "%s\n", strings.TrimSpace(cmd)); err != nil {
		return fmt.Errorf("liquidsoap telnet write cmd=%q: %w", cmd, err)
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		slog.Debug("Liquidsoap telnet response.", "cmd", cmd, "line", line)
		if line == "END" {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("liquidsoap telnet read cmd=%q: %w", cmd, err)
	}
	// EOF without END sentinel — treat as a protocol error.
	return fmt.Errorf("liquidsoap telnet: connection closed without END sentinel (cmd=%q)", cmd)
}
