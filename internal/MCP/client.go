package MCP

import (
	"CodeS/internal/tools"
	"context"
	"fmt"
	"net/http"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type staticToken struct {
	tokenSource oauth2.TokenSource
}

func newStaticToken(token, tokenType string) *staticToken {
	return new(staticToken{
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: token,
			TokenType:   tokenType,
		}),
	})
}

func (s *staticToken) TokenSource(_ context.Context) (oauth2.TokenSource, error) {
	return s.tokenSource, nil
}

func (s *staticToken) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("MCP authorize failed without HTTP response")
	}
	defer resp.Body.Close()
	return fmt.Errorf(
		"MCP server rejected the configured static token: %s",
		resp.Status,
	)
}

type Client struct {
	name     string
	endPoint string
	client   *sdkmcp.Client
	session  *sdkmcp.ClientSession
}

func New(name, endPoint string) *Client {
	_client := sdkmcp.NewClient(new(sdkmcp.Implementation{
		Name: "Codes",
	}), nil)

	return new(Client{
		name:     name,
		client:   _client,
		session:  nil,
		endPoint: endPoint,
	})
}

func (c *Client) Connect(ctx context.Context, tokenType, token string) error {
	if token == "" {
		return fmt.Errorf("%s's token not found", c.name)
	}
	transport := new(sdkmcp.StreamableClientTransport{
		Endpoint:     c.endPoint,
		OAuthHandler: newStaticToken(token, tokenType),
	})
	var err error
	c.session, err = c.client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("%s connect to mcp error: %s", c.name, err)
	}
	return nil
}

func (c *Client) Close() error {
	session := c.session
	c.session = nil
	if session == nil {
		return nil
	}
	return session.Close()
}

func (c *Client) GetTools(ctx context.Context, owner remoteToolCaller) ([]*RemoteTool, error) {
	ret := make([]*RemoteTool, 0, 50)
	for tool, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		ret = append(ret, new(RemoteTool{
			Owner:      owner,
			RemoteName: tool.Name,
			Def: tools.ToolDef{
				Name:              makePublicName(c.name, tool.Name),
				Description:       tool.Description,
				Properties:        mcpProperties(tool.InputSchema),
				Authority:         tools.CategoryCommand,
				Required:          mcpRequired(tool.InputSchema),
				CouldBeParalleled: false,
				Type:              "object",
			},
		}))
	}
	return ret, nil
}

func mcpProperties(schema any) map[string]any {
	if schema == nil {
		return nil
	}
	_sch, _ := schema.(map[string]any)
	ret, exist := _sch["properties"].(map[string]any)
	if !exist {
		return nil
	}
	return ret
}

func mcpRequired(schema any) []string {
	if schema == nil {
		return nil
	}
	_sch, _ := schema.(map[string]any)
	ret, exist := _sch["required"].([]string)
	if !exist {
		return nil
	}
	return ret
}

func (c *Client) callTool(ctx context.Context, args *sdkmcp.CallToolParams) ([]any, error) {
	result, err := c.session.CallTool(ctx, args)
	if err != nil {
		return nil, err
	}
	ret := make([]any, 0, len(result.Content))
	for _, content := range result.Content {
		ret = append(ret, content)
	}
	return ret, nil
}
