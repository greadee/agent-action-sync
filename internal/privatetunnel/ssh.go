// Package privatetunnel builds the narrowly scoped SSH forwarding command used
// to reach a SyncGate loopback listener from an operator's trusted laptop.
package privatetunnel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidRequest = errors.New("invalid private tunnel request")

type Request struct {
	SSHTarget  string
	LocalPort  int
	RemotePort int
}

// SSHArguments returns a no-shell, no-command SSH invocation that forwards a
// laptop loopback port to the home node's loopback administration listener.
func SSHArguments(request Request) ([]string, error) {
	if !validTarget(request.SSHTarget) || !validPort(request.LocalPort) || !validPort(request.RemotePort) {
		return nil, ErrInvalidRequest
	}
	forward := "127.0.0.1:" + strconv.Itoa(request.LocalPort) + ":127.0.0.1:" + strconv.Itoa(request.RemotePort)
	return []string{"-N", "-T", "-o", "ExitOnForwardFailure=yes", "-o", "ClearAllForwardings=yes", "-o", "RequestTTY=no", "-L", forward, request.SSHTarget}, nil
}

func validPort(value int) bool { return value >= 1 && value <= 65535 }

// Targets intentionally exclude options, whitespace, shell punctuation, and
// IPv6 literals. Use an SSH config host alias for complex connection setup.
func validTarget(value string) bool {
	if len(value) < 1 || len(value) > 253 || strings.HasPrefix(value, "-") || strings.Count(value, "@") > 1 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '_' || character == '@') {
			return false
		}
	}
	parts := strings.Split(value, "@")
	if len(parts) == 2 && (parts[0] == "" || parts[1] == "") {
		return false
	}
	return true
}

func Description(request Request) string {
	return fmt.Sprintf("laptop 127.0.0.1:%d -> home 127.0.0.1:%d", request.LocalPort, request.RemotePort)
}
