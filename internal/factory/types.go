package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("request not found")

type Request struct {
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	OwnerType     string         `json:"owner_type"`
	Owner         *string        `json:"owner"`
	SecretStorage string         `json:"secret_storage"`
	NotifyEmail   *string        `json:"notify_email"`
	Requester     *string        `json:"requester"`
	Source        map[string]any `json:"source"`
	Manifest      map[string]any `json:"manifest"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	StateNonce    string         `json:"state_nonce,omitempty"`
	App           map[string]any `json:"app,omitempty"`
	Delivery      map[string]any `json:"delivery,omitempty"`
	Simulated     bool           `json:"simulated,omitempty"`
	ApprovedBy    *string        `json:"approved_by,omitempty"`
	RejectReason  *string        `json:"reject_reason,omitempty"`
	Error         string         `json:"error,omitempty"`
}

type Credentials struct {
	ID            any    `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PrivateKey    string `json:"pem"`
	HTMLURL       string `json:"html_url"`
	Simulated     bool   `json:"simulated,omitempty"`
}

func (c Credentials) IDString() string {
	if c.ID == nil {
		return ""
	}
	return fmt.Sprint(c.ID)
}

func cloneRequest(in *Request) (*Request, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out Request
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
