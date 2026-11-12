package tools

import (
	"context"
	"errors"
	"time"
)

type Time struct{}

func (t Time) Definition() ToolDef {
	return ToolDef{
		Name:        "Time",
		Description: "获取当前时间。必须通过 zone 指定有效的 IANA 时区名称，例如 Asia/Shanghai；结果使用该时区。",
		Type:        "object",
		Properties: map[string]any{
			"zone": map[string]any{
				"type":        "string",
				"description": "必填的 IANA 时区名称，例如 Asia/Shanghai 或 Asia/Tokyo。",
			},
		},
		CouldBeParalleled: true,
		Authority:         CategoryRead,
	}
}

func (t Time) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	zone, ok := arguments["zone"].(string)
	if !ok {
		return nil, errors.New("argument 'zone' is not a string")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(loc)
	return []any{now}, nil

}
