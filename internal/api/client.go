package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"syncgate/internal/core"
)

const maxClientResponseBytes int64 = 4 << 20

type Client struct {
	baseURL    string
	credential []byte
	httpClient *http.Client
}

type ClientOptions struct {
	Address    string
	Credential []byte
	HTTPClient *http.Client
}

type ClientError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (clientError *ClientError) Error() string {
	if clientError == nil {
		return "local administration API request failed"
	}
	if clientError.Code == "" {
		return fmt.Sprintf("local administration API returned HTTP %d", clientError.StatusCode)
	}
	return fmt.Sprintf("local administration API %s: %s", clientError.Code, clientError.Message)
}

func NewClient(options ClientOptions) (*Client, error) {
	if err := validateLoopbackAddress(options.Address); err != nil {
		return nil, err
	}
	if err := validateAdminCredential(options.Credential); err != nil {
		return nil, fmt.Errorf("validate local administration credential: %w", err)
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL: "http://" + options.Address, credential: bytes.Clone(options.Credential), httpClient: httpClient,
	}, nil
}

func (client *Client) Close() {
	if client == nil {
		return
	}
	for index := range client.credential {
		client.credential[index] = 0
	}
	client.credential = nil
}

func (client *Client) Status(ctx context.Context) (AdminStatus, error) {
	var result AdminStatus
	err := client.do(ctx, http.MethodGet, "/api/v1/status", nil, &result)
	return result, err
}

func (client *Client) Diagnostics(ctx context.Context, limit int) (AdminDiagnostics, error) {
	path := "/api/v1/diagnostics"
	if limit > 0 {
		path += "?limit=" + url.QueryEscape(fmt.Sprint(limit))
	}
	var result AdminDiagnostics
	err := client.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (client *Client) RequestScan(ctx context.Context, shareID core.ShareID) (ScanAccepted, error) {
	if strings.TrimSpace(string(shareID)) == "" || strings.ContainsAny(string(shareID), "/\\") {
		return ScanAccepted{}, errors.New("share ID is invalid")
	}
	var result ScanAccepted
	err := client.do(ctx, http.MethodPost, "/api/v1/shares/"+url.PathEscape(string(shareID))+"/scans", struct{}{}, &result)
	return result, err
}

func (client *Client) ControlJob(ctx context.Context, jobID string, action JobActionName) (JobInventory, error) {
	if strings.TrimSpace(jobID) == "" || strings.ContainsAny(jobID, "/\\") {
		return JobInventory{}, errors.New("job ID is invalid")
	}
	var result JobInventory
	err := client.do(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/actions", JobAction{Action: action}, &result)
	return result, err
}

func (client *Client) CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error) {
	var result InvitationDTO
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/invitations", request, &result)
	return result, err
}

func (client *Client) InspectPairingInvitation(ctx context.Context, invitation string) (InvitationInspection, error) {
	var result InvitationInspection
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/inspect", EncodedInvitation{Invitation: invitation}, &result)
	return result, err
}

func (client *Client) AcceptPairingInvitation(ctx context.Context, request AcceptanceRequest) (Acceptance, error) {
	var result Acceptance
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/acceptances", request, &result)
	return result, err
}

func (client *Client) RevokePairingDevice(ctx context.Context, deviceID core.DeviceID) (Revocation, error) {
	if strings.TrimSpace(string(deviceID)) == "" || strings.ContainsAny(string(deviceID), "/\\") {
		return Revocation{}, errors.New("device ID is invalid")
	}
	var result Revocation
	err := client.do(ctx, http.MethodPost, "/api/v1/devices/"+url.PathEscape(string(deviceID))+"/revocation", struct{}{}, &result)
	return result, err
}

func (client *Client) do(ctx context.Context, method, path string, requestValue, responseValue any) error {
	if client == nil || len(client.credential) == 0 || client.httpClient == nil {
		return errors.New("local administration API client is closed or unconfigured")
	}
	if ctx == nil {
		return errors.New("local administration API context is required")
	}
	var body io.Reader
	if requestValue != nil {
		raw, err := json.Marshal(requestValue)
		if err != nil {
			return fmt.Errorf("encode local administration API request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create local administration API request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(client.credential))
	request.Header.Set("Accept", "application/json")
	if requestValue != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("connect to local administration API at %s: %w", client.baseURL, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxClientResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read local administration API response: %w", err)
	}
	if int64(len(raw)) > maxClientResponseBytes {
		return errors.New("local administration API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope ErrorEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return &ClientError{StatusCode: response.StatusCode}
		}
		return &ClientError{
			StatusCode: response.StatusCode, Code: envelope.Error.Code,
			Message: envelope.Error.Message, RequestID: envelope.Error.RequestID,
		}
	}
	if responseValue == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(responseValue); err != nil {
		return fmt.Errorf("decode local administration API response: %w", err)
	}
	return nil
}
