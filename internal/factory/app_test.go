package factory

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testApp(t *testing.T, extra map[string]string) (*App, *LocalStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"DATA_DIR":       dir,
		"INGEST_TOKEN":   "test-token",
		"ALLOW_SIMULATE": "true",
		"BASE_URL":       "https://factory.example.test",
	}
	for key, value := range extra {
		env[key] = value
	}
	app, err := New(Config{
		Env:       env,
		PublicDir: filepath.Join("..", "..", "public"),
		Store:     store,
		Delivery: &credentialDelivery{
			vault:  &mockVault{dir: dir},
			outbox: &localOutbox{dir: filepath.Join(dir, "outbox")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, store, dir
}

func issuePayload(name, secretStorage string) map[string]any {
	payload := map[string]any{
		"owner_type":     "org",
		"owner":          "my-org",
		"secret_storage": secretStorage,
		"requester":      "requester",
		"source": map[string]any{
			"type": "issue", "repo": "my-org/repo", "issue_number": 7,
			"url": "https://github.com/my-org/repo/issues/7",
		},
		"manifest": map[string]any{
			"name": name, "description": "Test app", "url": "https://example.test",
			"public": false, "default_permissions": map[string]any{"contents": "read"},
			"default_events": []any{"push"},
		},
	}
	if secretStorage == "email" {
		payload["notify_email"] = "team@example.test"
	}
	return payload
}

func callApp(t *testing.T, app *App, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

func decodeResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, response.Body.String())
	}
	return result
}

func ingest(t *testing.T, app *App, payload map[string]any) string {
	t.Helper()
	response := callApp(t, app, http.MethodPost, "/api/ingest", payload, map[string]string{
		"Authorization": "Bearer test-token",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("ingest status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeResponseInto(t, response, &result)
	if result.ID == "" || result.Status != "pending" {
		t.Fatalf("unexpected ingest response: %+v", result)
	}
	return result.ID
}

func decodeResponseInto(t *testing.T, response *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), value); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, response.Body.String())
	}
}

func TestIngestRequiresBearerTokenAndValidRequest(t *testing.T) {
	app, _, _ := testApp(t, nil)
	payload := issuePayload("demo-bot", "vault")
	if response := callApp(t, app, http.MethodPost, "/api/ingest", payload, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized ingest status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if response := callApp(t, app, http.MethodPost, "/api/ingest", payload, map[string]string{
		"Authorization": "Bearer wrong",
	}); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	payload["owner"] = "not valid!"
	if response := callApp(t, app, http.MethodPost, "/api/ingest", payload, map[string]string{
		"Authorization": "Bearer test-token",
	}); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestIngestListAndDuplicateDetection(t *testing.T) {
	app, _, _ := testApp(t, nil)
	payload := issuePayload("demo-bot", "vault")
	id := ingest(t, app, payload)

	duplicate := callApp(t, app, http.MethodPost, "/api/ingest", payload, map[string]string{
		"Authorization": "Bearer test-token",
	})
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want %d", duplicate.Code, http.StatusConflict)
	}
	var duplicateBody map[string]any
	decodeResponseInto(t, duplicate, &duplicateBody)
	if duplicateBody["id"] != id {
		t.Fatalf("duplicate id = %v, want %s", duplicateBody["id"], id)
	}

	list := callApp(t, app, http.MethodGet, "/api/requests", nil, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", list.Code, http.StatusOK)
	}
	rows := decodeResponse[[]map[string]any](t, list)
	if len(rows) != 1 || rows[0]["status"] != "pending" {
		t.Fatalf("unexpected list: %#v", rows)
	}
	if _, exists := rows[0]["state_nonce"]; exists {
		t.Fatal("request list leaked the manifest state nonce")
	}
}

func TestSimulateEmailCreatesZipOutboxWithoutPersistingCredentials(t *testing.T) {
	app, _, dir := testApp(t, nil)
	id := ingest(t, app, issuePayload("demo-bot", "email"))
	response := callApp(t, app, http.MethodPost, "/api/requests/"+id+"/simulate", map[string]any{}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("simulate status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var created Request
	decodeResponseInto(t, response, &created)
	if created.Status != "created" || !created.Simulated || created.Delivery["method"] != "email" {
		t.Fatalf("unexpected simulated request: %+v", created)
	}
	zipName, ok := created.Delivery["zipName"].(string)
	if !ok || zipName == "" {
		t.Fatalf("ZIP name missing from delivery: %#v", created.Delivery)
	}
	download := callApp(t, app, http.MethodGet, "/outbox/"+url.PathEscape(zipName), nil, nil)
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("ZIP download status/content type = %d/%q", download.Code, download.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(download.Body.Bytes()), int64(download.Body.Len()))
	if err != nil {
		t.Fatalf("read downloaded ZIP: %v", err)
	}
	foundKey := false
	for _, file := range archive.File {
		if file.Name == "demo-bot/private-key.pem" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Fatal("credential ZIP omitted the private key")
	}
	invalid := callApp(t, app, http.MethodGet, "/outbox/..%2Frequests.json", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid outbox path status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "requests.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "PRIVATE KEY") || strings.Contains(string(persisted), "client_secret") {
		t.Fatal("request store contains app credentials")
	}
	if info, err := os.Stat(filepath.Join(dir, "requests.json")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("request store mode = %o, want 600", info.Mode().Perm())
	}
}

func TestManifestFlowValidatesStateAndCompletesCallback(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/one-time-code/conversions" {
			t.Errorf("unexpected GitHub conversion request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":12345,"slug":"demo-bot","client_id":"Iv1.test","client_secret":"secret","pem":"private-key","html_url":"https://github.com/apps/demo-bot"}`)
	}))
	defer github.Close()

	app, store, _ := testApp(t, map[string]string{"GITHUB_API_URL": github.URL})
	id := ingest(t, app, issuePayload("demo-bot", "vault"))
	start := callApp(t, app, http.MethodPost, "/api/requests/"+id+"/start", map[string]any{}, map[string]string{
		"X-MS-CLIENT-PRINCIPAL-NAME": "approver@example.test",
	})
	if start.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", start.Code, start.Body.String())
	}
	var startBody struct {
		ActionURL string         `json:"action_url"`
		Manifest  map[string]any `json:"manifest"`
	}
	decodeResponseInto(t, start, &startBody)
	actionURL, err := url.Parse(startBody.ActionURL)
	if err != nil {
		t.Fatal(err)
	}
	state := actionURL.Query().Get("state")
	if !strings.HasPrefix(startBody.ActionURL, "https://github.com/organizations/my-org/settings/apps/new?state=") {
		t.Fatalf("unexpected GitHub App URL: %s", startBody.ActionURL)
	}
	if startBody.Manifest["redirect_url"] != "https://factory.example.test/callback" {
		t.Fatalf("callback URL = %v", startBody.Manifest["redirect_url"])
	}
	list := callApp(t, app, http.MethodGet, "/api/requests", nil, nil)
	if strings.Contains(list.Body.String(), "state_nonce") || strings.Contains(list.Body.String(), state) {
		t.Fatal("request list leaked the manifest state")
	}

	bad := callApp(t, app, http.MethodGet, "/callback?code=one-time-code&state="+url.QueryEscape(id+".bad"), nil, nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad callback state status = %d, want %d", bad.Code, http.StatusBadRequest)
	}
	callback := callApp(t, app, http.MethodGet, "/callback?code=one-time-code&state="+url.QueryEscape(state), nil, nil)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/#done-"+id {
		t.Fatalf("callback response = %d %q", callback.Code, callback.Header().Get("Location"))
	}
	completed, err := store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "created" || completed.StateNonce != "" || completed.ApprovedBy == nil || *completed.ApprovedBy != "approver@example.test" {
		t.Fatalf("unexpected completed request: %+v", completed)
	}
}

func TestDashboardHealthConfigAndRequestBodyLimit(t *testing.T) {
	app, _, _ := testApp(t, nil)
	if response := callApp(t, app, http.MethodGet, "/", nil, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "GitHub App Factory") {
		t.Fatalf("dashboard response = %d", response.Code)
	}
	health := callApp(t, app, http.MethodGet, "/api/health", nil, nil)
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"store":"local"`) {
		t.Fatalf("health response = %d %s", health.Code, health.Body.String())
	}
	config := callApp(t, app, http.MethodGet, "/api/config", nil, map[string]string{
		"X-MS-CLIENT-PRINCIPAL-NAME": "approver@example.test",
	})
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"user":"approver@example.test"`) {
		t.Fatalf("config response = %d %s", config.Code, config.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(bytes.Repeat([]byte("x"), requestBodyLimit+1)))
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}
