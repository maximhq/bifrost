package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// testAccount is an Account serving the MCP client records a test sets, as the live store serves
// them to the manager. It configures no providers.
type testAccount struct {
	mu      sync.RWMutex
	clients map[string]*schemas.MCPClientConfig
}

// newTestAccount returns a testAccount serving clients.
func newTestAccount(clients ...*schemas.MCPClientConfig) *testAccount {
	a := &testAccount{clients: make(map[string]*schemas.MCPClientConfig)}
	a.set(clients...)
	return a
}

// set stores each client as its record, replacing any record with the same ID. A record is
// read-only once set, as the live store's are: to change one, set a changed copy.
func (a *testAccount) set(clients ...*schemas.MCPClientConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, client := range clients {
		a.clients[client.ID] = client
	}
}

// remove drops the record with this ID.
func (a *testAccount) remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.clients, id)
}

// GetConfiguredProviders lists no providers: the account configures none.
func (a *testAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return nil, nil
}

// GetKeysForProvider finds no keys: the account configures no providers.
func (a *testAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return nil, nil
}

// GetConfigForProvider finds no provider: the account configures none.
func (a *testAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return nil, fmt.Errorf("provider %s is not configured", provider)
}

// GetConfiguredMCPClients lists the IDs of the records set.
func (a *testAccount) GetConfiguredMCPClients() ([]string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	ids := make([]string, 0, len(a.clients))
	for id := range a.clients {
		ids = append(ids, id)
	}
	return ids, nil
}

// GetConfigForMCPClient returns the record with this ID.
func (a *testAccount) GetConfigForMCPClient(id string) (*schemas.MCPClientConfig, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if client, ok := a.clients[id]; ok {
		return client, nil
	}
	return nil, fmt.Errorf("MCP client %s is not configured", id)
}

// GetConfigForMCPClientByName returns the record with this name.
func (a *testAccount) GetConfigForMCPClientByName(name string) (*schemas.MCPClientConfig, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, client := range a.clients {
		if client.Name == name {
			return client, nil
		}
	}
	return nil, fmt.Errorf("MCP client %s is not configured", name)
}

// accountOf returns the testAccount m reads its clients from.
func accountOf(m *MCPManager) *testAccount {
	return m.account.(*testAccount)
}

// addClient sets config as its client's record in m's test account and adds the client, as a caller
// writes the row, loads it, and then adds the client.
func addClient(ctx context.Context, m *MCPManager, config *schemas.MCPClientConfig) error {
	accountOf(m).set(config)
	return m.AddClient(ctx, config.ID)
}

// editedRecord returns a copy of the client's current record in m's test account with edit applied.
func editedRecord(m *MCPManager, id string, edit func(*schemas.MCPClientConfig)) (*schemas.MCPClientConfig, error) {
	current, err := accountOf(m).GetConfigForMCPClient(id)
	if err != nil {
		return nil, err
	}
	edited := *current
	edit(&edited)
	return &edited, nil
}

// editClient sets a copy of the client's record with edit applied and applies it with UpdateClient,
// as a caller writes an edited row, loads it, and then updates the client.
func editClient(m *MCPManager, id string, edit func(*schemas.MCPClientConfig)) error {
	edited, err := editedRecord(m, id, edit)
	if err != nil {
		return err
	}
	accountOf(m).set(edited)
	return m.UpdateClient(id)
}

// editClientCredentials is editClient for UpdateClientCredentials.
func editClientCredentials(m *MCPManager, id string, edit func(*schemas.MCPClientConfig)) error {
	edited, err := editedRecord(m, id, edit)
	if err != nil {
		return err
	}
	accountOf(m).set(edited)
	return m.UpdateClientCredentials(id)
}

// putClient seeds a client without dialing it: state's ExecutionConfig becomes the client's record in
// m's test account, and an entry built from the rest of state goes into the client map, as AddClient
// would have left it. It returns the entry.
func putClient(m *MCPManager, state *schemas.MCPClientState) *clientConn {
	config := state.ExecutionConfig
	accountOf(m).set(config)
	entry := &clientConn{
		Name:                state.Name,
		Conn:                state.Conn,
		ConnectionInfo:      state.ConnectionInfo,
		CancelFunc:          state.CancelFunc,
		State:               state.State,
		LastFailure:         state.LastFailure,
		ConnGeneration:      state.ConnGeneration,
		LastDiscoveryHash:   state.LastDiscoveryHash,
		dialed:              config,
		pendingVerification: config.PendingVerification,
	}
	if entry.Name == "" {
		entry.Name = config.Name
	}
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()
	return entry
}
