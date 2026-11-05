package factory

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

const requestBodyLimit = 1 << 20

var errPayloadTooLarge = errors.New("Payload too large")

var (
	slugPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	emailPattern      = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	outboxPathPattern = regexp.MustCompile(`^/outbox/([^/]+)$`)
	actionPathPattern = regexp.MustCompile(`^/api/requests/([\w-]+)/(start|simulate|reject|reset)$`)
)

type Config struct {
	Env        map[string]string
	PublicDir  string
	PublicFS   fs.FS
	Store      Store
	Delivery   Delivery
	HTTPClient *http.Client
}

type App struct {
	env           map[string]string
	publicFS      fs.FS
	store         Store
	delivery      Delivery
	httpClient    *http.Client
	ingestToken   string
	githubWebURL  string
	githubAPIURL  string
	githubToken   string
	baseURL       string
	allowSimulate bool
}

func New(cfg Config) (*App, error) {
	env := cfg.Env
	if env == nil {
		env = environment()
	}
	var err error
	dataDir := env["DATA_DIR"]
	if dataDir == "" {
		dataDir = "data"
	}
	publicFS := cfg.PublicFS
	if publicFS == nil {
		publicDir := cfg.PublicDir
		if publicDir == "" {
			publicDir = "public"
		}
		publicFS = os.DirFS(publicDir)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	needsAzureCredential := env["STORAGE_ACCOUNT_NAME"] != "" || env["KEY_VAULT_URL"] != ""
	var credential *azidentity.DefaultAzureCredential
	if needsAzureCredential && (cfg.Store == nil || cfg.Delivery == nil) {
		var err error
		credential, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("create Azure credential: %w", err)
		}
	}

	store := cfg.Store
	if store == nil {
		if account := env["STORAGE_ACCOUNT_NAME"]; account != "" {
			store, err = NewAzureTableStore(account, env["REQUESTS_TABLE"], credential)
			if err != nil {
				return nil, err
			}
		} else {
			store, err = NewLocalStore(dataDir)
			if err != nil {
				return nil, err
			}
		}
	}

	delivery := cfg.Delivery
	if delivery == nil {
		var vault secretStore
		if env["VAULT_ADDR"] != "" || env["VAULT_TOKEN"] != "" {
			if env["VAULT_ADDR"] == "" || env["VAULT_TOKEN"] == "" {
				return nil, errors.New("VAULT_ADDR and VAULT_TOKEN must be configured together")
			}
			vault = &hashicorpVault{
				baseURL: env["VAULT_ADDR"],
				token:   env["VAULT_TOKEN"],
				mount:   valueOr(env["VAULT_KV_MOUNT"], "secret"),
				prefix:  valueOr(env["VAULT_PATH_PREFIX"], "github-apps"),
				client:  httpClient,
			}
		} else if vaultURL := env["KEY_VAULT_URL"]; vaultURL != "" {
			client, err := azsecrets.NewClient(vaultURL, credential, nil)
			if err != nil {
				return nil, fmt.Errorf("create Azure Key Vault client: %w", err)
			}
			vault = &keyVault{client: client, url: vaultURL}
		} else {
			vault = &mockVault{dir: dataDir}
		}

		var outboxStore outbox
		if account := env["STORAGE_ACCOUNT_NAME"]; account != "" {
			blob, err := newAzureBlobOutbox(account, valueOr(env["OUTBOX_CONTAINER"], "outbox"), credential)
			if err != nil {
				return nil, err
			}
			outboxStore = blob
		} else {
			outboxStore = &localOutbox{dir: filepath.Join(dataDir, "outbox")}
		}
		delivery = &credentialDelivery{vault: vault, outbox: outboxStore}
	}

	allowSimulate := env["ALLOW_SIMULATE"] != "false"
	return &App{
		env:           env,
		publicFS:      publicFS,
		store:         store,
		delivery:      delivery,
		httpClient:    httpClient,
		ingestToken:   valueOr(env["INGEST_TOKEN"], "dev-ingest-token"),
		githubWebURL:  strings.TrimRight(valueOr(env["GITHUB_WEB_URL"], "https://github.com"), "/"),
		githubAPIURL:  strings.TrimRight(valueOr(env["GITHUB_API_URL"], "https://api.github.com"), "/"),
		githubToken:   env["GITHUB_TOKEN"],
		baseURL:       strings.TrimRight(env["BASE_URL"], "/"),
		allowSimulate: allowSimulate,
	}, nil
}

func environment() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (a *App) StoreKind() string {
	return a.store.Kind()
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	if r.Method == http.MethodGet && (path == "/" || path == "/index.html") {
		a.serveIndex(w)
		return
	}
	if r.Method == http.MethodGet && path == "/api/health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "store": a.store.Kind()})
		return
	}
	if r.Method == http.MethodGet && path == "/api/config" {
		user := r.Header.Get("X-MS-CLIENT-PRINCIPAL-NAME")
		writeJSON(w, http.StatusOK, map[string]any{
			"vault_provider": a.delivery.VaultProvider(),
			"outbox":         a.delivery.OutboxKind(),
			"store":          a.store.Kind(),
			"github_web":     a.githubWebURL,
			"issue_comments": a.githubToken != "",
			"allow_simulate": a.allowSimulate,
			"user":           stringPointer(user),
		})
		return
	}
	if r.Method == http.MethodPost && path == "/api/ingest" {
		a.ingest(w, r)
		return
	}
	if r.Method == http.MethodGet && path == "/api/requests" {
		a.listRequests(w, r)
		return
	}
	if r.Method == http.MethodGet && path == "/callback" {
		a.callback(w, r)
		return
	}
	if r.Method == http.MethodGet {
		if match := outboxPathPattern.FindStringSubmatch(path); match != nil {
			a.getOutbox(w, r, match[1])
			return
		}
	}
	if r.Method == http.MethodPost {
		if match := actionPathPattern.FindStringSubmatch(path); match != nil {
			a.requestAction(w, r, match[1], match[2])
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
}

func (a *App) serveIndex(w http.ResponseWriter) {
	data, err := fs.ReadFile(a.publicFS, "index.html")
	if err != nil {
		log.Printf("read dashboard: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("write dashboard response: %v", err)
	}
}

func (a *App) ingest(w http.ResponseWriter, r *http.Request) {
	if !constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+a.ingestToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	body, err := decodeBody(w, r)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	manifest, _ := body["manifest"].(map[string]any)
	if problems := validateRequest(body, manifest); len(problems) != 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": problems})
		return
	}
	rows, err := a.store.List(r.Context())
	if err != nil {
		a.internalError(w, "list requests before ingest", err)
		return
	}
	name := stringValue(manifest["name"])
	for _, existing := range rows {
		if existing.Status != "rejected" && strings.EqualFold(stringValue(existing.Manifest["name"]), name) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": fmt.Sprintf("Request for '%s' already exists", name),
				"id":    existing.ID,
			})
			return
		}
	}

	ownerType := stringValue(body["owner_type"])
	var owner *string
	if ownerType == "org" {
		owner = stringPointer(stringValue(body["owner"]))
	}
	source, ok := body["source"].(map[string]any)
	if !ok || source == nil {
		source = map[string]any{"type": "api"}
	} else {
		source = cloneMap(source)
	}
	nowValue := now()
	id, err := newUUID()
	if err != nil {
		a.internalError(w, "create request id", err)
		return
	}
	row := &Request{
		ID:            id,
		Status:        "pending",
		OwnerType:     ownerType,
		Owner:         owner,
		SecretStorage: stringValue(body["secret_storage"]),
		NotifyEmail:   stringPointer(stringValue(body["notify_email"])),
		Requester:     stringPointer(stringValue(body["requester"])),
		Source:        source,
		Manifest:      cloneMap(manifest),
		CreatedAt:     nowValue,
		UpdatedAt:     nowValue,
	}
	if err := a.store.Insert(r.Context(), row); err != nil {
		a.internalError(w, "insert request", err)
		return
	}
	log.Printf("queued request %s for app %q", row.ID, name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         row.ID,
		"status":     row.Status,
		"review_url": a.baseURLFor(r) + "/#" + row.ID,
	})
}

func (a *App) listRequests(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.List(r.Context())
	if err != nil {
		a.internalError(w, "list requests", err)
		return
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	for _, row := range rows {
		row.StateNonce = ""
	}
	writeJSON(w, http.StatusOK, rows)
}

func (a *App) requestAction(w http.ResponseWriter, r *http.Request, id, action string) {
	body, err := decodeBody(w, r)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	row, err := a.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Request not found"})
		return
	}
	if err != nil {
		a.internalError(w, "get request for action", err)
		return
	}
	who := stringPointer(r.Header.Get("X-MS-CLIENT-PRINCIPAL-NAME"))

	switch action {
	case "reject":
		reason := stringPointer(stringValue(body["reason"]))
		updated, err := a.store.Update(r.Context(), id, func(current *Request) error {
			current.Status = "rejected"
			current.RejectReason = reason
			current.ApprovedBy = who
			return nil
		})
		if err != nil {
			a.internalError(w, "reject request", err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	case "reset":
		updated, err := a.store.Update(r.Context(), id, func(current *Request) error {
			current.Status = "pending"
			current.StateNonce = ""
			return nil
		})
		if err != nil {
			a.internalError(w, "reset request", err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}

	if row.Status == "created" || row.Status == "rejected" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Request already " + row.Status})
		return
	}
	if action == "simulate" && !a.allowSimulate {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Simulation disabled"})
		return
	}

	storage := stringValue(body["secret_storage"])
	if storage == "" {
		storage = row.SecretStorage
	}
	email := stringValue(body["notify_email"])
	if email == "" && row.NotifyEmail != nil {
		email = *row.NotifyEmail
	}
	if storage != "vault" && storage != "email" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid secret_storage"})
		return
	}
	if storage == "email" && !emailPattern.MatchString(email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Valid email required"})
		return
	}
	row, err = a.store.Update(r.Context(), id, func(current *Request) error {
		current.SecretStorage = storage
		current.NotifyEmail = stringPointer(email)
		return nil
	})
	if err != nil {
		a.internalError(w, "update delivery preferences", err)
		return
	}

	if action == "simulate" {
		credentials, err := fakeCredentials(row, a.githubWebURL)
		if err != nil {
			a.internalError(w, "create simulated credentials", err)
			return
		}
		updated, err := a.finalize(r.Context(), row, credentials, who)
		if err != nil {
			a.internalError(w, "finalize simulated request", err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}

	nonce, err := randomHex(16)
	if err != nil {
		a.internalError(w, "create manifest state", err)
		return
	}
	row, err = a.store.Update(r.Context(), id, func(current *Request) error {
		current.Status = "awaiting_github"
		current.StateNonce = nonce
		current.ApprovedBy = who
		return nil
	})
	if err != nil {
		a.internalError(w, "start GitHub manifest flow", err)
		return
	}
	manifest := cloneMap(row.Manifest)
	manifest["redirect_url"] = a.baseURLFor(r) + "/callback"
	writeJSON(w, http.StatusOK, map[string]any{
		"action_url": a.githubNewAppURL(row, row.ID+"."+nonce),
		"manifest":   manifest,
	})
}

func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := strings.SplitN(r.URL.Query().Get("state"), ".", 2)
	if code == "" || len(state) != 2 || state[0] == "" || state[1] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired state"})
		return
	}
	row, err := a.store.Get(r.Context(), state[0])
	if errors.Is(err, ErrNotFound) || (err == nil && (row.StateNonce == "" || !constantTimeEqual(row.StateNonce, state[1]))) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired state"})
		return
	}
	if err != nil {
		a.internalError(w, "get callback request", err)
		return
	}
	endpoint := a.githubAPIURL + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, nil)
	if err != nil {
		a.internalError(w, "create GitHub conversion request", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := a.httpClient.Do(req)
	if err != nil {
		a.internalError(w, "convert GitHub manifest", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if readErr != nil {
			a.internalError(w, "read GitHub conversion error", readErr)
			return
		}
		_, updateErr := a.store.Update(r.Context(), row.ID, func(current *Request) error {
			current.Status = "failed"
			current.Error = string(detail)
			current.StateNonce = ""
			return nil
		})
		if updateErr != nil {
			a.internalError(w, "record GitHub conversion failure", updateErr)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Manifest conversion failed",
			"detail": string(detail),
		})
		return
	}
	var credentials Credentials
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&credentials); err != nil {
		a.internalError(w, "decode GitHub conversion response", err)
		return
	}
	updated, err := a.finalize(r.Context(), row, credentials, stringPointer(r.Header.Get("X-MS-CLIENT-PRINCIPAL-NAME")))
	if err != nil {
		a.internalError(w, "finalize GitHub App creation", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/#done-"+updated.ID)
	w.WriteHeader(http.StatusFound)
}

func (a *App) finalize(ctx context.Context, row *Request, credentials Credentials, who *string) (*Request, error) {
	var deliveryInfo map[string]any
	var err error
	switch row.SecretStorage {
	case "vault":
		deliveryInfo, err = a.delivery.StoreInVault(ctx, credentials)
		if err == nil {
			deliveryInfo["method"] = "vault"
			deliveryInfo["provider"] = a.delivery.VaultProvider()
		}
	case "email":
		if row.NotifyEmail == nil || !emailPattern.MatchString(*row.NotifyEmail) {
			return nil, errors.New("valid recipient email is required")
		}
		deliveryInfo, err = a.delivery.EmailZip(ctx, credentials, *row.NotifyEmail)
		if err == nil {
			deliveryInfo["method"] = "email"
		}
	default:
		return nil, errors.New("unsupported secret delivery method")
	}
	if err != nil {
		return nil, err
	}
	approvedBy := who
	if approvedBy == nil {
		approvedBy = row.ApprovedBy
	}
	updated, err := a.store.Update(ctx, row.ID, func(current *Request) error {
		current.Status = "created"
		current.StateNonce = ""
		current.ApprovedBy = approvedBy
		current.App = map[string]any{
			"id":        credentials.ID,
			"slug":      credentials.Slug,
			"client_id": credentials.ClientID,
			"html_url":  credentials.HTMLURL,
		}
		current.Delivery = deliveryInfo
		current.Simulated = credentials.Simulated
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("save completed request: %w", err)
	}
	a.commentOnIssue(ctx, updated, credentials)
	return updated, nil
}

func (a *App) commentOnIssue(ctx context.Context, row *Request, credentials Credentials) {
	if a.githubToken == "" || stringValue(row.Source["type"]) != "issue" {
		return
	}
	repo := stringValue(row.Source["repo"])
	number := fmt.Sprint(row.Source["issue_number"])
	if repo == "" || number == "" || number == "<nil>" {
		return
	}
	endpoint := a.githubAPIURL + "/repos/" + repo + "/issues/" + url.PathEscape(number) + "/comments"
	simulated := ""
	if credentials.Simulated {
		simulated = " (simulated)"
	}
	recipient := ""
	if row.SecretStorage == "email" && row.NotifyEmail != nil {
		recipient = " to " + *row.NotifyEmail
	}
	body := map[string]string{"body": fmt.Sprintf(
		"✅ GitHub App **%s** (ID %s) was created%s.\nCredentials delivered via **%s**%s.",
		credentials.Slug, credentials.IDString(), simulated, row.SecretStorage, recipient,
	)}
	raw, err := json.Marshal(body)
	if err != nil {
		log.Printf("encode GitHub issue comment: %v", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		log.Printf("create GitHub issue comment request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.githubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	response, err := a.httpClient.Do(req)
	if err != nil {
		log.Printf("GitHub issue comment failed: %v", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.Printf("GitHub issue comment failed with HTTP %d", response.StatusCode)
	}
}

func (a *App) getOutbox(w http.ResponseWriter, r *http.Request, escapedName string) {
	name, err := url.PathUnescape(escapedName)
	if err != nil || !outboxNamePattern.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid name"})
		return
	}
	data, err := a.delivery.GetOutbox(r.Context(), name)
	if errors.Is(err, ErrOutboxNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
		return
	}
	if err != nil {
		a.internalError(w, "read credential outbox item", err)
		return
	}
	contentType := "message/rfc822"
	if strings.HasSuffix(name, ".zip") {
		contentType = "application/zip"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("write outbox response: %v", err)
	}
}

func (a *App) githubNewAppURL(row *Request, state string) string {
	path := "/settings/apps/new"
	if row.OwnerType == "org" && row.Owner != nil {
		path = "/organizations/" + url.PathEscape(*row.Owner) + "/settings/apps/new"
	}
	return a.githubWebURL + path + "?state=" + url.QueryEscape(state)
}

func (a *App) baseURLFor(r *http.Request) string {
	if a.baseURL != "" {
		return a.baseURL
	}
	proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	return proto + "://" + host
}

func firstHeaderValue(value string) string {
	value, _, _ = strings.Cut(value, ",")
	return strings.TrimSpace(value)
}

func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	reader := http.MaxBytesReader(w, r.Body, requestBodyLimit)
	raw, err := io.ReadAll(reader)
	if err != nil {
		var limitErr *http.MaxBytesError
		if errors.As(err, &limitErr) {
			return nil, errPayloadTooLarge
		}
		return nil, errors.New("Could not read request body")
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("Invalid JSON")
	}
	return body, nil
}

func writeBodyError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errPayloadTooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func validateRequest(body, manifest map[string]any) []string {
	problems := make([]string, 0)
	name := stringValue(manifest["name"])
	if name == "" || len([]rune(name)) > 34 {
		problems = append(problems, "manifest.name is required (max 34 chars)")
	}
	if stringValue(manifest["url"]) == "" {
		problems = append(problems, "manifest.url (homepage) is required")
	}
	ownerType := stringValue(body["owner_type"])
	if ownerType != "org" && ownerType != "user" {
		problems = append(problems, "owner_type must be 'org' or 'user'")
	}
	if ownerType == "org" && !slugPattern.MatchString(stringValue(body["owner"])) {
		problems = append(problems, "owner (org login) is invalid")
	}
	secretStorage := stringValue(body["secret_storage"])
	if secretStorage != "vault" && secretStorage != "email" {
		problems = append(problems, "secret_storage must be 'vault' or 'email'")
	}
	if secretStorage == "email" && !emailPattern.MatchString(stringValue(body["notify_email"])) {
		problems = append(problems, "notify_email is required for email delivery")
	}
	return problems
}

func constantTimeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func fakeCredentials(row *Request, githubWebURL string) (Credentials, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate simulated private key: %w", err)
	}
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	slug := slugify(stringValue(row.Manifest["name"]))
	id, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return Credentials{}, fmt.Errorf("generate simulated app id: %w", err)
	}
	clientID, err := randomHex(8)
	if err != nil {
		return Credentials{}, err
	}
	clientSecret, err := randomHex(20)
	if err != nil {
		return Credentials{}, err
	}
	webhookSecret := ""
	if row.Manifest["hook_attributes"] != nil {
		webhookSecret, err = randomHex(16)
		if err != nil {
			return Credentials{}, err
		}
	}
	return Credentials{
		ID:            100000 + id.Int64(),
		Slug:          slug,
		ClientID:      "Iv1." + clientID,
		ClientSecret:  clientSecret,
		WebhookSecret: webhookSecret,
		PrivateKey:    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})),
		HTMLURL:       strings.TrimRight(githubWebURL, "/") + "/apps/" + slug,
		Simulated:     true,
	}, nil
}

func slugify(value string) string {
	var out strings.Builder
	lastHyphen := false
	for _, char := range strings.ToLower(value) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			out.WriteRune(char)
			lastHyphen = false
		default:
			if out.Len() > 0 && !lastHyphen {
				out.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func randomHex(bytesCount int) (string, error) {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func (a *App) internalError(w http.ResponseWriter, operation string, err error) {
	log.Printf("%s: %v", operation, err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal error"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		log.Printf("encode JSON response: %v", err)
		status = http.StatusInternalServerError
		raw = []byte(`{"error":"Internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(raw); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}
