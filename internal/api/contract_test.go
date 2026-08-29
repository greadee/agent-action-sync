package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type contractRoute struct {
	method, operationID, path               string
	public, browser, browserRead, paginated bool
}

var expectedContractRoutes = []contractRoute{
	{method: "get", operationID: "getHealth", path: "/healthz", public: true},
	{method: "post", operationID: "createBrowserSession", path: "/api/v1/browser-sessions"},
	{method: "post", operationID: "exchangeBrowserSession", path: "/api/v1/browser-session/bootstrap", public: true},
	{method: "get", operationID: "getBrowserSession", path: "/api/v1/browser-session", browser: true},
	{method: "get", operationID: "getStatus", path: "/api/v1/status"},
	{method: "get", operationID: "getDiagnostics", path: "/api/v1/diagnostics"},
	{method: "get", operationID: "listShares", path: "/api/v1/shares", paginated: true},
	{method: "get", operationID: "listDevices", path: "/api/v1/devices", paginated: true},
	{method: "get", operationID: "getDevice", path: "/api/v1/devices/{device_id}"},
	{method: "get", operationID: "listJobs", path: "/api/v1/jobs", paginated: true},
	{method: "get", operationID: "getJob", path: "/api/v1/jobs/{job_id}"},
	{method: "get", operationID: "listAuditEvents", path: "/api/v1/audit-events", paginated: true},
	{method: "post", operationID: "preflightProjectMigration", path: "/api/v1/project-migrations/preflight"},
	{method: "post", operationID: "applyProjectMigration", path: "/api/v1/project-migrations/apply"},
	{method: "get", operationID: "listProjects", path: "/api/v1/projects", paginated: true},
	{method: "get", operationID: "getProject", path: "/api/v1/projects/{project_id}"},
	{method: "get", operationID: "listProjectHistory", path: "/api/v1/projects/{project_id}/history", paginated: true},
	{method: "get", operationID: "listProjectArtifacts", path: "/api/v1/projects/{project_id}/artifacts", paginated: true},
	{method: "get", operationID: "listProjectInsights", path: "/api/v1/projects/{project_id}/insights", paginated: true},
	{method: "get", operationID: "listProjectRejections", path: "/api/v1/projects/{project_id}/rejections", paginated: true},
	{method: "post", operationID: "rebuildProjectProjections", path: "/api/v1/projects/{project_id}/projections/rebuild"},
	{method: "get", operationID: "listSetupTasks", path: "/api/v1/projects/{project_id}/tasks", browserRead: true, paginated: true},
	{method: "post", operationID: "createTaskGraph", path: "/api/v1/projects/{project_id}/tasks"},
	{method: "post", operationID: "validateTaskGraph", path: "/api/v1/projects/{project_id}/tasks/validate"},
	{method: "get", operationID: "getTaskReadiness", path: "/api/v1/projects/{project_id}/tasks/{task_id}/readiness", browserRead: true, paginated: true},
	{method: "post", operationID: "preflightContext", path: "/api/v1/projects/{project_id}/context/preflight"},
	{method: "post", operationID: "preflightRuntime", path: "/api/v1/projects/{project_id}/runtime/preflight"},
	{method: "post", operationID: "previewExecutionContract", path: "/api/v1/projects/{project_id}/execution-contracts/preview"},
	{method: "get", operationID: "listExecutionTelemetry", path: "/api/v1/projects/{project_id}/telemetry", paginated: true},
	{method: "get", operationID: "listTrades", path: "/api/v1/orchestration/trades", paginated: true},
	{method: "get", operationID: "listWorkers", path: "/api/v1/orchestration/workers", browserRead: true, paginated: true},
	{method: "get", operationID: "getSetupCapabilities", path: "/api/v1/orchestration/capabilities"},
	{method: "get", operationID: "listLocalProjects", path: "/api/v1/orchestration/projects", browserRead: true, paginated: true},
	{method: "post", operationID: "selectLocalProject", path: "/api/v1/orchestration/projects/{project_id}/selection"},
	{method: "put", operationID: "setLocalProjectPolicy", path: "/api/v1/orchestration/projects/{project_id}/policy"},
	{method: "get", operationID: "listOrchestrationNodes", path: "/api/v1/orchestration/nodes", browserRead: true, paginated: true},
	{method: "post", operationID: "approveTaskGraph", path: "/api/v1/projects/{project_id}/tasks/{task_id}/approve"},
	{method: "post", operationID: "previewDispatch", path: "/api/v1/projects/{project_id}/dispatch/preview"},
	{method: "post", operationID: "startOrchestrationScheduler", path: "/api/v1/projects/{project_id}/scheduler/start"},
	{method: "post", operationID: "disableOrchestrationScheduler", path: "/api/v1/projects/{project_id}/scheduler/disable"},
	{method: "get", operationID: "listAssignments", path: "/api/v1/projects/{project_id}/assignments", browserRead: true, paginated: true},
	{method: "get", operationID: "getAssignment", path: "/api/v1/projects/{project_id}/assignments/{assignment_id}", browserRead: true},
	{method: "post", operationID: "controlAssignment", path: "/api/v1/projects/{project_id}/assignments/{assignment_id}/controls"},
	{method: "post", operationID: "decideIntegration", path: "/api/v1/projects/{project_id}/assignments/{assignment_id}/integration"},
	{method: "post", operationID: "startShareScan", path: "/api/v1/shares/{share_id}/scans"},
	{method: "post", operationID: "actOnJob", path: "/api/v1/jobs/{job_id}/actions"},
	{method: "post", operationID: "createPairingInvitation", path: "/api/v1/pairing/invitations"},
	{method: "post", operationID: "inspectPairingInvitation", path: "/api/v1/pairing/inspect"},
	{method: "post", operationID: "acceptPairingInvitation", path: "/api/v1/pairing/acceptances"},
	{method: "post", operationID: "revokeDevice", path: "/api/v1/devices/{device_id}/revocation"},
}

func TestLocalAdminAPIContract(t *testing.T) {
	contractPath := filepath.Join("..", "..", "docs", "protocol", "local-admin-api-openapi.json")
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("contract must be valid JSON: %v", err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("expected OpenAPI 3.1.0, got %v", document["openapi"])
	}
	servers := requiredSlice(t, document, "servers")
	serverObject, ok := servers[0].(map[string]any)
	if !ok {
		t.Fatal("servers entries must be objects")
	}
	serverURL, ok := serverObject["url"].(string)
	if !ok {
		t.Fatal("server url must be a string")
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("API server must be loopback-only: %v", serverURL)
	}

	components := requiredMap(t, document, "components")
	securitySchemes := requiredMap(t, components, "securitySchemes")
	if _, ok := securitySchemes["bearerAuth"]; !ok {
		t.Fatal("contract must define bearerAuth")
	}
	if _, ok := securitySchemes["browserSession"]; !ok {
		t.Fatal("contract must define browserSession")
	}
	parameters := requiredMap(t, components, "parameters")
	limit := requiredMap(t, parameters, "Limit")
	limitSchema := requiredMap(t, limit, "schema")
	if limitSchema["maximum"] != float64(200) || limitSchema["default"] != float64(50) {
		t.Fatalf("pagination limit must default to 50 and cap at 200: %#v", limitSchema)
	}
	for _, parameterName := range []string{"EventType", "WorkPackageID", "ExecutionID", "MediaType", "Scope", "MetricName"} {
		parameter := requiredMap(t, parameters, parameterName)
		schema := requiredMap(t, parameter, "schema")
		if schema["maxLength"] != float64(256) {
			t.Errorf("%s must be bounded to 256 bytes: %#v", parameterName, schema)
		}
	}
	schemas := requiredMap(t, components, "schemas")
	for schemaName, forbidden := range map[string][]string{
		"Project":          {"root_path", "manifest_path", "manifest_record_hash"},
		"ProjectHistory":   {"payload_json"},
		"ProjectArtifact":  {"blob_path"},
		"ProjectRejection": {"quarantine_path", "content", "payload_json"},
	} {
		schema := requiredMap(t, schemas, schemaName)
		if schema["additionalProperties"] != false {
			t.Errorf("%s must reject additional response fields", schemaName)
		}
		properties := requiredMap(t, schema, "properties")
		for _, field := range forbidden {
			if _, ok := properties[field]; ok {
				t.Errorf("%s must not expose %s", schemaName, field)
			}
		}
	}

	paths := requiredMap(t, document, "paths")
	seenOperations := map[string]bool{}
	for _, route := range expectedContractRoutes {
		pathValue, ok := paths[route.path]
		if !ok {
			t.Errorf("missing route %s %s", strings.ToUpper(route.method), route.path)
			continue
		}
		pathItem, ok := pathValue.(map[string]any)
		if !ok {
			t.Errorf("path item %s must be an object", route.path)
			continue
		}
		operation, ok := pathItem[route.method].(map[string]any)
		if !ok {
			t.Errorf("missing method %s on %s", strings.ToUpper(route.method), route.path)
			continue
		}
		if operation["operationId"] != route.operationID {
			t.Errorf("%s %s has operationId %v, want %s", strings.ToUpper(route.method), route.path, operation["operationId"], route.operationID)
		}
		if seenOperations[route.operationID] {
			t.Errorf("duplicate operationId %s", route.operationID)
		}
		seenOperations[route.operationID] = true
		responses := requiredMap(t, operation, "responses")
		if len(responses) == 0 {
			t.Errorf("%s %s has no responses", strings.ToUpper(route.method), route.path)
		}
		if route.public {
			security, ok := operation["security"].([]any)
			if !ok || len(security) != 0 {
				t.Errorf("public bootstrap or health route must explicitly disable security")
			}
		} else if route.browser {
			if !hasSecurityScheme(operation["security"], "browserSession") {
				t.Errorf("%s %s must declare browserSession", strings.ToUpper(route.method), route.path)
			}
		} else if !hasBearerSecurity(operation["security"]) {
			t.Errorf("%s %s must declare bearerAuth", strings.ToUpper(route.method), route.path)
		}
		if route.browserRead && !hasSecurityScheme(operation["security"], "browserSession") {
			t.Errorf("%s %s must declare browserSession for the bounded visibility shell", strings.ToUpper(route.method), route.path)
		}
		if route.paginated && !hasParameterRefs(operation, "Limit", "Cursor") {
			t.Errorf("%s %s must expose bounded limit and cursor pagination", strings.ToUpper(route.method), route.path)
		}
	}

	for path := range paths {
		lower := strings.ToLower(path)
		for _, forbidden := range []string{"file", "content", "config", "download", "upload", "remote", "shell", "listen"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("forbidden administrative surface in path %q", path)
			}
		}
	}
}

func requiredMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s must be an object", key)
	}
	return value
}

func requiredSlice(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok || len(value) == 0 {
		t.Fatalf("%s must be a non-empty array", key)
	}
	return value
}

func hasBearerSecurity(value any) bool {
	return hasSecurityScheme(value, "bearerAuth")
}

func hasSecurityScheme(value any, name string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			if _, ok := object[name]; ok {
				return true
			}
		}
	}
	return false
}

func hasParameterRefs(operation map[string]any, names ...string) bool {
	parameters, ok := operation["parameters"].([]any)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, parameter := range parameters {
		object, ok := parameter.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := object["$ref"].(string)
		if !ok {
			continue
		}
		seen[filepath.Base(ref)] = true
	}
	for _, name := range names {
		if !seen[name] {
			return false
		}
	}
	return true
}

func Example() {
	fmt.Println("loopback-only, bearer-authenticated, versioned")
	// Output: loopback-only, bearer-authenticated, versioned
}
