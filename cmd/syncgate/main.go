package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/transfer"
	tcptls "syncgate/internal/transport/tcp"
)

func main() {
	if len(os.Args) < 2 {
		runCheckConfig([]string{})
		return
	}

	switch os.Args[1] {
	case "check-config":
		runCheckConfig(os.Args[2:])
	case "receive-once":
		runReceiveOnce(os.Args[2:])
	case "send-once":
		runSendOnce(os.Args[2:])
	default:
		exitf("unknown command %q", os.Args[1])
	}
}

func runCheckConfig(args []string) {
	flags := flag.NewFlagSet("check-config", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	_ = flags.Parse(args)

	cfg, err := config.LoadFile(context.Background(), *configPath)
	if err != nil {
		exitf("%v", err)
	}

	fmt.Printf("syncgate config ok: device=%s shares=%d\n", cfg.DeviceName, len(cfg.Shares))
}

func runReceiveOnce(args []string) {
	flags := flag.NewFlagSet("receive-once", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:47821", "manual TCP/TLS listen address")
	shareRoot := flags.String("share-root", "", "destination share root")
	deviceID := flags.String("device-id", "RECEIVER", "local device ID for development TLS")
	_ = flags.Parse(args)
	if *shareRoot == "" {
		exitf("--share-root is required")
	}

	tlsConfig, err := tcptls.DevTLSConfig(core.DeviceID(*deviceID), true)
	if err != nil {
		exitf("%v", err)
	}
	server := tcptls.New(*listen, tlsConfig)
	ctx := context.Background()
	sessions, err := server.Listen(ctx)
	if err != nil {
		exitf("%v", err)
	}
	defer server.Close()
	fmt.Printf("syncgate receiving on %s\n", server.Address)

	session, ok := <-sessions
	if !ok {
		exitf("listener closed before receiving a session")
	}
	defer session.Close()
	stream, err := session.AcceptStream(ctx)
	if err != nil {
		exitf("%v", err)
	}
	result, err := transfer.ReceiveFile(stream, *shareRoot)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("received %s bytes=%d path=%s\n", result.Manifest.RelativePath, result.BytesReceived, result.DestinationPath)
}

func runSendOnce(args []string) {
	flags := flag.NewFlagSet("send-once", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:47821", "manual TCP/TLS receiver address")
	source := flags.String("file", "", "source file to send")
	relativePath := flags.String("relative-path", "", "destination relative path inside receiver share")
	deviceID := flags.String("device-id", "SENDER", "local device ID for development TLS")
	chunkSize := flags.Int64("chunk-size", transfer.DefaultChunkSize, "fixed transfer chunk size")
	_ = flags.Parse(args)
	if *source == "" {
		exitf("--file is required")
	}
	if *relativePath == "" {
		exitf("--relative-path is required")
	}

	tlsConfig, err := tcptls.DevTLSConfig(core.DeviceID(*deviceID), false)
	if err != nil {
		exitf("%v", err)
	}
	client := tcptls.New(*addr, tlsConfig)
	session, err := client.Connect(context.Background(), "")
	if err != nil {
		exitf("%v", err)
	}
	defer session.Close()
	stream, err := session.OpenStream(context.Background())
	if err != nil {
		exitf("%v", err)
	}
	result, err := transfer.SendFile(stream, *source, *relativePath, *chunkSize)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("sent %s bytes=%d hash=%s\n", result.Manifest.RelativePath, result.BytesSent, result.Manifest.ContentHash)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "syncgate: "+format+"\n", args...)
	os.Exit(1)
}
