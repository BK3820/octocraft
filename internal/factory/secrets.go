package factory

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

var ErrOutboxNotFound = errors.New("outbox item not found")

type outbox interface {
	Kind() string
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}

type secretStore interface {
	Provider() string
	Store(context.Context, Credentials) (map[string]any, error)
}

type Delivery interface {
	VaultProvider() string
	OutboxKind() string
	StoreInVault(context.Context, Credentials) (map[string]any, error)
	EmailZip(context.Context, Credentials, string) (map[string]any, error)
	GetOutbox(context.Context, string) ([]byte, error)
}

type credentialDelivery struct {
	vault  secretStore
	outbox outbox
}

func (d *credentialDelivery) VaultProvider() string {
	return d.vault.Provider()
}

func (d *credentialDelivery) OutboxKind() string {
	return d.outbox.Kind()
}

func (d *credentialDelivery) StoreInVault(ctx context.Context, creds Credentials) (map[string]any, error) {
	return d.vault.Store(ctx, creds)
}

func (d *credentialDelivery) EmailZip(ctx context.Context, creds Credentials, recipient string) (map[string]any, error) {
	zipData, err := buildZip(creds)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().UnixMilli()
	zipName := fmt.Sprintf("%s-%d.zip", creds.Slug, stamp)
	emlName := fmt.Sprintf("%s-%d.eml", creds.Slug, stamp)
	if err := d.outbox.Put(ctx, zipName, zipData); err != nil {
		return nil, fmt.Errorf("write credential ZIP to outbox: %w", err)
	}
	if err := d.outbox.Put(ctx, emlName, []byte(buildEml(creds.Slug, recipient, zipData, stamp))); err != nil {
		return nil, fmt.Errorf("write email draft to outbox: %w", err)
	}
	return map[string]any{"to": recipient, "zipName": zipName, "emlName": emlName}, nil
}

func (d *credentialDelivery) GetOutbox(ctx context.Context, name string) ([]byte, error) {
	return d.outbox.Get(ctx, name)
}

type localOutbox struct {
	dir string
}

func (o *localOutbox) Kind() string {
	return "local"
}

func (o *localOutbox) Put(_ context.Context, name string, data []byte) error {
	if !outboxNamePattern.MatchString(name) {
		return fmt.Errorf("invalid outbox filename")
	}
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		return fmt.Errorf("create outbox directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(o.dir, name), data, 0o600); err != nil {
		return fmt.Errorf("write outbox item: %w", err)
	}
	return nil
}

func (o *localOutbox) Get(_ context.Context, name string) ([]byte, error) {
	if !outboxNamePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid outbox filename")
	}
	data, err := os.ReadFile(filepath.Join(o.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrOutboxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read outbox item: %w", err)
	}
	return data, nil
}

type azureBlobOutbox struct {
	client    *azblob.Client
	container string
	initOnce  sync.Once
	initErr   error
}

func newAzureBlobOutbox(account, container string, credential azcore.TokenCredential) (*azureBlobOutbox, error) {
	if container == "" {
		container = "outbox"
	}
	client, err := azblob.NewClient("https://"+account+".blob.core.windows.net", credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure Blob client: %w", err)
	}
	return &azureBlobOutbox{client: client, container: container}, nil
}

func (o *azureBlobOutbox) Kind() string {
	return "azure-blob"
}

func (o *azureBlobOutbox) ensure(ctx context.Context) error {
	o.initOnce.Do(func() {
		_, err := o.client.CreateContainer(ctx, o.container, nil)
		if isStatus(err, 409) {
			return
		}
		o.initErr = err
	})
	if o.initErr != nil {
		return fmt.Errorf("ensure Azure Blob container %q: %w", o.container, o.initErr)
	}
	return nil
}

func (o *azureBlobOutbox) Put(ctx context.Context, name string, data []byte) error {
	if err := o.ensure(ctx); err != nil {
		return err
	}
	if _, err := o.client.UploadBuffer(ctx, o.container, name, data, nil); err != nil {
		return fmt.Errorf("upload Azure Blob outbox item: %w", err)
	}
	return nil
}

func (o *azureBlobOutbox) Get(ctx context.Context, name string) ([]byte, error) {
	if err := o.ensure(ctx); err != nil {
		return nil, err
	}
	response, err := o.client.DownloadStream(ctx, o.container, name, nil)
	if isStatus(err, 404) {
		return nil, ErrOutboxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("download Azure Blob outbox item: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Azure Blob outbox item: %w", err)
	}
	return data, nil
}

type mockVault struct {
	dir string
}

func (v *mockVault) Provider() string {
	return "mock"
}

func (v *mockVault) Store(_ context.Context, creds Credentials) (map[string]any, error) {
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create mock vault directory: %w", err)
	}
	file := filepath.Join(v.dir, "mock-vault.json")
	vault := map[string]any{}
	raw, err := os.ReadFile(file)
	if err == nil {
		if err := json.Unmarshal(raw, &vault); err != nil {
			return nil, fmt.Errorf("decode mock vault: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read mock vault: %w", err)
	}
	location := "secret/github-apps/" + creds.Slug
	vault[location] = map[string]any{
		"app_id":         creds.IDString(),
		"slug":           creds.Slug,
		"client_id":      creds.ClientID,
		"client_secret":  creds.ClientSecret,
		"webhook_secret": creds.WebhookSecret,
		"private_key":    creds.PrivateKey,
		"written_at":     now(),
	}
	encoded, err := json.MarshalIndent(vault, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode mock vault: %w", err)
	}
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write mock vault: %w", err)
	}
	return map[string]any{"location": "mock-vault → " + location}, nil
}

type hashicorpVault struct {
	baseURL string
	token   string
	mount   string
	prefix  string
	client  *http.Client
}

func (v *hashicorpVault) Provider() string {
	return "hashicorp"
}

func (v *hashicorpVault) Store(ctx context.Context, creds Credentials) (map[string]any, error) {
	secretPath := strings.Trim(v.prefix, "/") + "/" + creds.Slug
	endpoint := strings.TrimRight(v.baseURL, "/") + "/v1/" + url.PathEscape(v.mount) + "/data/" + secretPath
	payload := map[string]any{"data": map[string]string{
		"app_id":         creds.IDString(),
		"slug":           creds.Slug,
		"client_id":      creds.ClientID,
		"client_secret":  creds.ClientSecret,
		"webhook_secret": creds.WebhookSecret,
		"private_key":    creds.PrivateKey,
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Vault payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create Vault request: %w", err)
	}
	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("write secret to Vault: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Vault write returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Data struct {
			Version int64 `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Vault write response: %w", err)
	}
	return map[string]any{
		"location": v.baseURL + " → " + v.mount + "/" + secretPath,
		"version":  result.Data.Version,
	}, nil
}

type keyVault struct {
	client *azsecrets.Client
	url    string
}

func (v *keyVault) Provider() string {
	return "keyvault"
}

func (v *keyVault) Store(ctx context.Context, creds Credentials) (map[string]any, error) {
	base := "ghapp-" + creds.Slug
	base = keyVaultNamePattern.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 90 {
		base = base[:90]
	}
	tags := map[string]*string{
		"app_id":    to.Ptr(creds.IDString()),
		"client_id": to.Ptr(creds.ClientID),
		"slug":      to.Ptr(creds.Slug),
	}
	items := []struct {
		suffix      string
		value       string
		contentType string
	}{
		{"private-key", creds.PrivateKey, "application/x-pem-file"},
		{"client-secret", creds.ClientSecret, "text/plain"},
	}
	if creds.WebhookSecret != "" {
		items = append(items, struct {
			suffix      string
			value       string
			contentType string
		}{"webhook-secret", creds.WebhookSecret, "text/plain"})
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := base + "-" + item.suffix
		_, err := v.client.SetSecret(ctx, name, azsecrets.SetSecretParameters{
			Value:       to.Ptr(item.value),
			ContentType: to.Ptr(item.contentType),
			Tags:        tags,
		}, nil)
		if err != nil {
			return nil, fmt.Errorf("store secret %q in Key Vault: %w", name, err)
		}
		names = append(names, name)
	}
	return map[string]any{
		"location": strings.TrimRight(v.url, "/") + " → " + strings.Join(names, ", "),
	}, nil
}

func buildZip(creds Credentials) ([]byte, error) {
	meta := map[string]any{
		"app_id":         creds.ID,
		"slug":           creds.Slug,
		"client_id":      creds.ClientID,
		"client_secret":  creds.ClientSecret,
		"webhook_secret": nil,
		"html_url":       creds.HTMLURL,
	}
	if creds.WebhookSecret != "" {
		meta["webhook_secret"] = creds.WebhookSecret
	}
	metadata, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode credential metadata: %w", err)
	}
	files := []struct {
		name    string
		content []byte
	}{
		{creds.Slug + "/app-credentials.json", metadata},
		{creds.Slug + "/private-key.pem", []byte(creds.PrivateKey)},
		{creds.Slug + "/README.txt", []byte(fmt.Sprintf(
			"GitHub App: %s\nApp ID: %s\n\nStore these credentials in a secret manager and delete this archive.\n",
			creds.Slug, creds.IDString(),
		))},
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range files {
		entry, err := writer.Create(file.name)
		if err != nil {
			return nil, fmt.Errorf("create credential ZIP entry: %w", err)
		}
		if _, err := entry.Write(file.content); err != nil {
			return nil, fmt.Errorf("write credential ZIP entry: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish credential ZIP: %w", err)
	}
	return output.Bytes(), nil
}

func buildEml(slug, recipient string, zipData []byte, stamp int64) string {
	boundary := fmt.Sprintf("----gaf%d", stamp)
	encoded := base64.StdEncoding.EncodeToString(zipData)
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76])
		wrapped.WriteString("\r\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded)
	return strings.Join([]string{
		"To: " + recipient,
		"From: github-app-factory@localhost",
		"Subject: Credentials for GitHub App " + slug,
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="` + boundary + `"`,
		"",
		"--" + boundary,
		"Content-Type: text/plain; charset=utf-8",
		"",
		fmt.Sprintf(`Your GitHub App "%s" has been created. Credentials are attached.`, slug),
		"",
		"--" + boundary,
		`Content-Type: application/zip; name="` + slug + `.zip"`,
		"Content-Transfer-Encoding: base64",
		`Content-Disposition: attachment; filename="` + slug + `.zip"`,
		"",
		wrapped.String(),
		"--" + boundary + "--",
		"",
	}, "\r\n")
}

var (
	outboxNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*\.(zip|eml)$`)
	keyVaultNamePattern = regexp.MustCompile(`[^A-Za-z0-9-]+`)
)
