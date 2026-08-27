package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSendsCredentialInHeaderAndDecodesCommands(t *testing.T) {
	credential := clientTestCredential(0x51)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+string(credential) {
			t.Fatal("client did not send bearer credential")
		}
		if request.URL.RawQuery != "" && strings.Contains(request.URL.RawQuery, string(credential)) {
			t.Fatal("client placed credential in query string")
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/browser-sessions":
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(BrowserSessionTicketResponse{BootstrapToken: "bootstrap", ExpiresAt: time.Now().UTC()})
		case "/api/v1/status":
			_ = json.NewEncoder(writer).Encode(AdminStatus{Status: "running", APIVersion: APIVersion})
		case "/api/v1/shares/drop/scans":
			writer.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(writer).Encode(ScanAccepted{Accepted: true, ShareID: "drop"})
		case "/api/v1/jobs/job-1/actions":
			_ = json.NewEncoder(writer).Encode(JobInventory{ID: "job-1", State: "paused"})
		case "/api/v1/project-migrations/preflight":
			_ = json.NewEncoder(writer).Encode(ProjectMigrationPreflight{Status: "ready", Confirmation: strings.Repeat("a", 64)})
		case "/api/v1/project-migrations/apply":
			_ = json.NewEncoder(writer).Encode(ProjectMigrationApplyResult{Status: "applied", ProjectID: "project-1"})
		default:
			http.NotFound(writer, request)
		}
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	client, err := NewClient(ClientOptions{Address: listener.Addr().String(), Credential: credential})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	status, err := client.Status(context.Background())
	if err != nil || status.APIVersion != APIVersion {
		t.Fatalf("Status = %+v, err=%v", status, err)
	}
	ticket, err := client.CreateBrowserSession(context.Background())
	if err != nil || ticket.BootstrapToken != "bootstrap" {
		t.Fatalf("CreateBrowserSession = %+v, err=%v", ticket, err)
	}
	scan, err := client.RequestScan(context.Background(), "drop")
	if err != nil || !scan.Accepted {
		t.Fatalf("RequestScan = %+v, err=%v", scan, err)
	}
	job, err := client.ControlJob(context.Background(), "job-1", JobActionPause)
	if err != nil || job.State != "paused" {
		t.Fatalf("ControlJob = %+v, err=%v", job, err)
	}
	preflight, err := client.PreflightProjectMigration(context.Background(), ProjectMigrationInput{ShareID: "drop", ProjectID: "project-1", Name: "Project"})
	if err != nil || preflight.Status != "ready" {
		t.Fatalf("PreflightProjectMigration = %+v, err=%v", preflight, err)
	}
	applied, err := client.ApplyProjectMigration(context.Background(), ProjectMigrationApplyInput{ProjectMigrationInput: ProjectMigrationInput{ShareID: "drop", ProjectID: "project-1", Name: "Project"}, Confirmation: preflight.Confirmation})
	if err != nil || applied.Status != "applied" {
		t.Fatalf("ApplyProjectMigration = %+v, err=%v", applied, err)
	}
}

func TestClientReturnsSanitizedAPIErrorAndRejectsRemoteAddress(t *testing.T) {
	credential := clientTestCredential(0x52)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeError(writer, request, errUnauthorized)
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	client, err := NewClient(ClientOptions{Address: listener.Addr().String(), Credential: credential})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Status(context.Background())
	clientError, ok := err.(*ClientError)
	if !ok || clientError.StatusCode != http.StatusUnauthorized || clientError.Code != "unauthorized" {
		t.Fatalf("client error = %#v", err)
	}
	if strings.Contains(err.Error(), string(credential)) {
		t.Fatal("client error disclosed credential")
	}
	if _, err := NewClient(ClientOptions{Address: "192.0.2.1:47820", Credential: credential}); err == nil {
		t.Fatal("client accepted a non-loopback address")
	}
	client.Close()
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("closed client accepted a request")
	}
}

func clientTestCredential(value byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)))
}
