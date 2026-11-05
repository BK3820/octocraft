package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

func TestMountedTokenFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("mounted-test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, _, _ := testApp(t, map[string]string{
		"INGEST_TOKEN": "", "INGEST_TOKEN_FILE": file,
		"GITHUB_TOKEN_FILE": file,
	})
	if app.ingestToken != "mounted-test-token" || app.githubToken != "mounted-test-token" {
		t.Fatal("mounted tokens were not loaded")
	}
	if response := callApp(t, app, http.MethodPost, "/api/ingest", issuePayload("mounted-bot", "vault"),
		map[string]string{"Authorization": "Bearer mounted-test-token"}); response.Code != http.StatusCreated {
		t.Fatalf("mounted ingestion token rejected: %d", response.Code)
	}

	for _, name := range []string{"INGEST_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"", "token\n", "token with spaces"} {
				if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := secretValue(map[string]string{name + "_FILE": file}, name); err == nil {
					t.Fatalf("invalid token file accepted: %q", value)
				}
			}
			if _, err := secretValue(map[string]string{name + "_FILE": file + "-missing"}, name); err == nil {
				t.Fatal("missing token file accepted")
			}
			if _, err := secretValue(map[string]string{name: "env-token", name + "_FILE": file}, name); err == nil {
				t.Fatal("simultaneous token sources accepted")
			}
		})
	}
	if _, err := New(Config{Env: map[string]string{"INGEST_TOKEN_FILE": file + "-missing"}}); err == nil {
		t.Fatal("initialization silently accepted a missing mounted token")
	}
}

func TestVaultOnlyRequiresRealConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"VAULT_ONLY": "true", "INGEST_TOKEN": "test-token"},
		{"VAULT_ONLY": "true", "KEY_VAULT_URL": "https://example-vault.vault.azure.net"},
		{"VAULT_ONLY": "true", "KEY_VAULT_URL": "https://example-vault.vault.azure.net", "VAULT_ADDR": "https://vault.example.test", "INGEST_TOKEN": "test-token"},
	} {
		if _, err := New(Config{Env: env}); err == nil {
			t.Fatalf("invalid vault-only configuration accepted: %v", env)
		}
	}
}

func TestVaultOnlyBlocksEmailAndHistoricalDownloads(t *testing.T) {
	app, store, dir := testApp(t, map[string]string{
		"VAULT_ONLY": "true", "KEY_VAULT_URL": "https://example-vault.vault.azure.net",
	})
	email := issuePayload("email-bot", "email")
	if response := callApp(t, app, http.MethodPost, "/api/ingest", email,
		map[string]string{"Authorization": "Bearer test-token"}); response.Code != http.StatusForbidden {
		t.Fatalf("email ingestion status = %d", response.Code)
	}
	id := ingest(t, app, issuePayload("vault-bot", "vault"))
	for _, action := range []string{"start", "simulate"} {
		response := callApp(t, app, http.MethodPost, "/api/requests/"+id+"/"+action,
			map[string]any{"secret_storage": "email", "notify_email": "team@example.test"}, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("email %s status = %d", action, response.Code)
		}
	}
	row, err := store.Update(t.Context(), id, func(row *Request) error {
		row.SecretStorage = "email"
		row.StateNonce = "historical-state"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	response := callApp(t, app, http.MethodGet, "/callback?code=unused&state="+url.QueryEscape(id+".historical-state"), nil, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("historical email callback status = %d", response.Code)
	}
	if _, err := app.finalize(t.Context(), row, Credentials{}, nil); err == nil {
		t.Fatal("finalization bypassed vault-only policy")
	}
	if response := callApp(t, app, http.MethodGet, "/outbox/old.zip", nil, nil); response.Code != http.StatusForbidden {
		t.Fatalf("historical download status = %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "outbox")); !os.IsNotExist(err) {
		t.Fatalf("unexpected credential outbox: %v", err)
	}
	config := decodeResponse[map[string]any](t, callApp(t, app, http.MethodGet, "/api/config", nil, nil))
	if config["vault_only"] != true {
		t.Fatal("vault-only policy not exposed to UI")
	}
}

type podTestCredential struct{}

func (podTestCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "synthetic-access-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestPVCAndKeyVaultWithoutStorageAccount(t *testing.T) {
	stored := map[string]string{}
	failWrites := false
	vault := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer authorization="https://login.microsoftonline.com/test", resource="https://vault.azure.net"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if failWrites {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":"Forbidden","message":"test write denied"}}`)
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("unexpected vault method: %s", r.Method)
		}
		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		stored[r.URL.Path] = body.Value
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"https://example-vault.vault.azure.net/secrets/test/version"}`)
	}))
	defer vault.Close()

	dir := t.TempDir()
	app, err := New(Config{Env: map[string]string{
		"DATA_DIR": dir, "KEY_VAULT_URL": vault.URL, "VAULT_ONLY": "true",
		"INGEST_TOKEN": "test-token",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if app.StoreKind() != "local" || app.delivery.VaultProvider() != "keyvault" || app.delivery.OutboxKind() != "local" {
		t.Fatal("PVC/Key Vault configuration selected the wrong providers")
	}
	client, err := azsecrets.NewClient(vault.URL, podTestCredential{}, &azsecrets.ClientOptions{
		ClientOptions:                        azcore.ClientOptions{Transport: vault.Client()},
		DisableChallengeResourceVerification: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.delivery.(*credentialDelivery).vault.(*keyVault).client = client

	id := ingest(t, app, issuePayload("vault-bot", "vault"))
	row, err := app.store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{
		ID: json.Number("123"), Slug: "vault-bot", ClientID: "synthetic-client-id",
		ClientSecret: "synthetic-client-secret", PrivateKey: "synthetic-private-key",
		WebhookSecret: "synthetic-webhook-secret",
	}
	completed, err := app.finalize(t.Context(), row, creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "created" || completed.Delivery["provider"] != "keyvault" {
		t.Fatal("Key Vault completion was not persisted")
	}
	for suffix, value := range map[string]string{
		"private-key": creds.PrivateKey, "client-secret": creds.ClientSecret, "webhook-secret": creds.WebhookSecret,
	} {
		if stored["/secrets/ghapp-vault-bot-"+suffix] != value {
			t.Fatalf("expected credential was not written to Key Vault: %s", suffix)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "requests.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{creds.ClientSecret, creds.PrivateKey, creds.WebhookSecret} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential leaked into PVC request metadata")
		}
	}
	failWrites = true
	if _, err := app.finalize(t.Context(), row, creds, nil); err == nil {
		t.Fatal("Key Vault write failure was hidden")
	}
	for _, name := range []string{"mock-vault.json", "outbox"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("credential filesystem fallback detected: %s: %v", name, err)
		}
	}
}
