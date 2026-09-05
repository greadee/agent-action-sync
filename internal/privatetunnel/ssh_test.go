package privatetunnel

import (
	"errors"
	"reflect"
	"testing"
)

func TestSSHArgumentsOnlyCreatesLoopbackForwardWithoutRemoteCommand(t *testing.T) {
	arguments, err := SSHArguments(Request{SSHTarget: "operator@home-node", LocalPort: 47820, RemotePort: 47821})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-N", "-T", "-o", "ExitOnForwardFailure=yes", "-o", "ClearAllForwardings=yes", "-o", "RequestTTY=no", "-L", "127.0.0.1:47820:127.0.0.1:47821", "operator@home-node"}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestSSHArgumentsRejectUnsafeTargetsAndPorts(t *testing.T) {
	for _, request := range []Request{
		{SSHTarget: "-oProxyCommand=bad", LocalPort: 1, RemotePort: 1}, {SSHTarget: "home node", LocalPort: 1, RemotePort: 1},
		{SSHTarget: "operator@", LocalPort: 1, RemotePort: 1}, {SSHTarget: "home", LocalPort: 0, RemotePort: 1}, {SSHTarget: "home", LocalPort: 1, RemotePort: 65536},
	} {
		if _, err := SSHArguments(request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request=%+v err=%v", request, err)
		}
	}
}
