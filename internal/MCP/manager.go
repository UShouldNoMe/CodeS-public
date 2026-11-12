package MCP

import (
	"CodeS/config"
	"CodeS/internal/tools"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	StateConfigured ServerState = "Configured"
	StateConnected  ServerState = "Connected"
	StateFailed     ServerState = "Failed"
	StateClosed     ServerState = "Closed"
)

var ErrManagerClosed = errors.New("mcp manager is closed")

type ServerState string

type serverEntity struct {
	config config.MCPServerConfig
	client *Client
	tools  []*RemoteTool
	state  ServerState
	err    error
	mu     sync.RWMutex
}

type Manager struct {
	lifeCycleMu     sync.Mutex
	servers         map[string]*serverEntity
	serverWithOrder []string
	closed          bool
	closeErr        error
}

func (s *serverEntity) CallTool(ctx context.Context, arguments *sdkmcp.CallToolParams) ([]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateConnected {
		return nil, fmt.Errorf("mcp server %q is not connected", s.client.name)
	}
	return s.client.callTool(ctx, arguments)
}

func NewManager(_configs []config.MCPServerConfig) (*Manager, error) {
	ret := new(Manager{
		servers:         make(map[string]*serverEntity),
		serverWithOrder: make([]string, 0),
		lifeCycleMu:     sync.Mutex{},
	})
	for _, cfg := range _configs {
		if cfg.Name == "" || cfg.Endpoint == "" {
			return nil, fmt.Errorf("mcp server config must have name and endpoint")
		}
		if cfg.Auth.TokenEnv == "" {
			return nil, fmt.Errorf("mcp server config must have token env")
		}
		if _, exists := ret.servers[cfg.Name]; exists {
			return nil, fmt.Errorf("mcp server config name %s already exists", cfg.Name)
		}
		ret.serverWithOrder = append(ret.serverWithOrder, cfg.Name)
		ret.servers[cfg.Name] = &serverEntity{
			config: cfg,
			client: New(cfg.Name, cfg.Endpoint),
			tools:  make([]*RemoteTool, 0),
			state:  StateConfigured,
			err:    nil,
			mu:     sync.RWMutex{},
		}
	}
	return ret, nil
}

func (m *Manager) ConnectAll(ctx context.Context) error {
	m.lifeCycleMu.Lock()
	defer m.lifeCycleMu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	return m.connectAll(ctx)
}

func (m *Manager) connectAll(ctx context.Context) error {
	ch := make(chan error, len(m.serverWithOrder)*2)
	wg := sync.WaitGroup{}
	for _, server := range m.serverWithOrder {
		wg.Go(func() {
			entity := m.servers[server]
			entity.mu.Lock()
			defer entity.mu.Unlock()
			if entity.state == StateConnected {
				return
			}
			_err := entity.client.Connect(ctx, entity.config.Auth.Type, os.Getenv(entity.config.Auth.TokenEnv))
			if _err != nil {
				ch <- _err
				entity.state = StateFailed
				entity.tools = nil
				entity.err = _err
				return
			}
			tool, err := entity.client.GetTools(ctx, entity)
			entity.state = StateConnected
			entity.tools = tool
			entity.err = err
			if err != nil {
				entity.state = StateFailed
				closeErr := entity.client.Close()
				entity.err = errors.Join(err, closeErr)
				ch <- entity.err
				return
			}
		})
	}
	wg.Wait()
	close(ch)
	var errs []error
	for err := range ch {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) CloseAll() error {
	m.lifeCycleMu.Lock()
	defer m.lifeCycleMu.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	var errs []error
	for _, server := range m.serverWithOrder {
		entity := m.servers[server]
		entity.mu.Lock()
		err := entity.client.Close()
		if err != nil {
			errs = append(errs, err)
		}
		entity.state = StateClosed
		entity.err = err
		entity.tools = nil
		entity.mu.Unlock()
	}
	m.closeErr = errors.Join(errs...)
	return m.closeErr
}

func (m *Manager) GetTools() []tools.Tool {
	var ret []tools.Tool
	for _, serverName := range m.serverWithOrder {
		entity := m.servers[serverName]
		entity.mu.RLock()
		if entity.state != StateConnected {
			entity.mu.RUnlock()
			continue
		}
		for _, remoteTool := range entity.tools {
			ret = append(ret, remoteTool)
		}
		entity.mu.RUnlock()
	}
	return ret
}
