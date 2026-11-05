package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

type Store interface {
	Kind() string
	List(context.Context) ([]*Request, error)
	Get(context.Context, string) (*Request, error)
	Insert(context.Context, *Request) error
	Update(context.Context, string, func(*Request) error) (*Request, error)
}

type LocalStore struct {
	mu   sync.Mutex
	file string
}

func NewLocalStore(dataDir string) (*LocalStore, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create local data directory: %w", err)
	}
	return &LocalStore{file: filepath.Join(dataDir, "requests.json")}, nil
}

func (s *LocalStore) Kind() string {
	return "local"
}

func (s *LocalStore) load() ([]*Request, error) {
	raw, err := os.ReadFile(s.file)
	if errors.Is(err, os.ErrNotExist) {
		return []*Request{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read request store: %w", err)
	}
	var rows []*Request
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode request store: %w", err)
	}
	if rows == nil {
		rows = []*Request{}
	}
	return rows, nil
}

func (s *LocalStore) save(rows []*Request) error {
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("encode request store: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(s.file), ".requests-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary request store: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temporary request store: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary request store: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("flush temporary request store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary request store: %w", err)
	}
	if err := os.Rename(tempName, s.file); err != nil {
		return fmt.Errorf("replace request store: %w", err)
	}
	return nil
}

func (s *LocalStore) List(_ context.Context) ([]*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	out := make([]*Request, 0, len(rows))
	for _, row := range rows {
		copy, err := cloneRequest(row)
		if err != nil {
			return nil, fmt.Errorf("copy stored request: %w", err)
		}
		out = append(out, copy)
	}
	return out, nil
}

func (s *LocalStore) Get(_ context.Context, id string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.ID == id {
			return cloneRequest(row)
		}
	}
	return nil, ErrNotFound
}

func (s *LocalStore) Insert(_ context.Context, row *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range rows {
		if existing.ID == row.ID {
			return fmt.Errorf("request id already exists")
		}
	}
	copy, err := cloneRequest(row)
	if err != nil {
		return fmt.Errorf("copy request for storage: %w", err)
	}
	rows = append(rows, copy)
	return s.save(rows)
}

func (s *LocalStore) Update(_ context.Context, id string, mutate func(*Request) error) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		if row.ID != id {
			continue
		}
		copy, err := cloneRequest(row)
		if err != nil {
			return nil, fmt.Errorf("copy stored request: %w", err)
		}
		if err := mutate(copy); err != nil {
			return nil, err
		}
		copy.UpdatedAt = now()
		rows[i] = copy
		if err := s.save(rows); err != nil {
			return nil, err
		}
		return cloneRequest(copy)
	}
	return nil, ErrNotFound
}

type AzureTableStore struct {
	service  *aztables.ServiceClient
	table    *aztables.Client
	name     string
	initOnce sync.Once
	initErr  error
}

func NewAzureTableStore(account, tableName string, credential azcore.TokenCredential) (*AzureTableStore, error) {
	if tableName == "" {
		tableName = "apprequests"
	}
	endpoint := "https://" + account + ".table.core.windows.net"
	service, err := aztables.NewServiceClient(endpoint, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure Table service client: %w", err)
	}
	table := service.NewClient(tableName)
	return &AzureTableStore{service: service, table: table, name: tableName}, nil
}

func (s *AzureTableStore) Kind() string {
	return "azure-table"
}

func (s *AzureTableStore) ensure(ctx context.Context) error {
	s.initOnce.Do(func() {
		_, err := s.service.CreateTable(ctx, s.name, nil)
		if isStatus(err, 409) {
			return
		}
		s.initErr = err
	})
	if s.initErr != nil {
		return fmt.Errorf("ensure Azure Table %q: %w", s.name, s.initErr)
	}
	return nil
}

type tableEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	Status       string `json:"status"`
	Data         string `json:"data"`
}

func (s *AzureTableStore) List(ctx context.Context) ([]*Request, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	filter := "PartitionKey eq 'request'"
	pager := s.table.NewListEntitiesPager(&aztables.ListEntitiesOptions{Filter: to.Ptr(filter)})
	rows := make([]*Request, 0)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list Azure Table requests: %w", err)
		}
		for _, raw := range page.Entities {
			row, err := decodeTableEntity(raw)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	return rows, nil
}

func (s *AzureTableStore) Get(ctx context.Context, id string) (*Request, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	response, err := s.table.GetEntity(ctx, "request", id, nil)
	if isStatus(err, 404) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Azure Table request: %w", err)
	}
	return decodeTableEntity(response.Value)
}

func (s *AzureTableStore) Insert(ctx context.Context, row *Request) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	raw, err := encodeTableEntity(row)
	if err != nil {
		return err
	}
	if _, err := s.table.AddEntity(ctx, raw, nil); err != nil {
		return fmt.Errorf("insert Azure Table request: %w", err)
	}
	return nil
}

func (s *AzureTableStore) Update(ctx context.Context, id string, mutate func(*Request) error) (*Request, error) {
	row, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := mutate(row); err != nil {
		return nil, err
	}
	row.UpdatedAt = now()
	raw, err := encodeTableEntity(row)
	if err != nil {
		return nil, err
	}
	if _, err := s.table.UpsertEntity(ctx, raw, &aztables.UpsertEntityOptions{UpdateMode: aztables.UpdateModeReplace}); err != nil {
		return nil, fmt.Errorf("update Azure Table request: %w", err)
	}
	return row, nil
}

func encodeTableEntity(row *Request) ([]byte, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("encode request for Azure Table: %w", err)
	}
	entity, err := json.Marshal(tableEntity{
		PartitionKey: "request",
		RowKey:       row.ID,
		Status:       row.Status,
		Data:         string(raw),
	})
	if err != nil {
		return nil, fmt.Errorf("encode Azure Table entity: %w", err)
	}
	return entity, nil
}

func decodeTableEntity(raw []byte) (*Request, error) {
	var entity tableEntity
	if err := json.Unmarshal(raw, &entity); err != nil {
		return nil, fmt.Errorf("decode Azure Table entity: %w", err)
	}
	var row Request
	if err := json.Unmarshal([]byte(entity.Data), &row); err != nil {
		return nil, fmt.Errorf("decode request from Azure Table: %w", err)
	}
	return &row, nil
}

func isStatus(err error, status int) bool {
	var responseErr *azcore.ResponseError
	return errors.As(err, &responseErr) && responseErr.StatusCode == status
}
