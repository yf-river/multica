package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issuePropertyFilterPattern = regexp.MustCompile(`^([^<>=!~]+)(>=|<=|~=|!=|=|>|<)(.*)$`)

// propertyComparison 校验属性类型对应的操作，重复属性由服务端按 OR 组合。
func propertyComparison(kind, operator, value string) (any, error) {
	if operator == "=" && value == "__none__" {
		return value, nil
	}
	if operator == "!=" {
		return nil, fmt.Errorf("不支持 !=；服务端没有不等筛选")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" && kind != "text" {
		return nil, fmt.Errorf("筛选值不能为空")
	}
	op := ""
	switch kind {
	case "number":
		v, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("数字属性需要有限数值")
		}
		op = map[string]string{">": "gt", ">=": "gte", "<": "lt", "<=": "lte"}[operator]
	case "date":
		if _, err := time.Parse("2006-01-02", trimmed); err != nil {
			return nil, fmt.Errorf("日期格式必须为 YYYY-MM-DD")
		}
		op = map[string]string{">": "after", "<": "before"}[operator]
	case "text", "url":
		if operator == "~=" {
			op = "contains"
		}
	}
	if operator == "=" {
		return value, nil
	}
	if op == "" {
		return nil, fmt.Errorf("%s 属性不支持 %s；日期只支持严格的 > 和 <，其他非比较类型只支持 =", kind, operator)
	}
	return map[string]string{"op": op, "value": value}, nil
}

func resolveIssuePropertyFilters(ctx context.Context, client *cli.APIClient, pairs []string) (string, error) {
	properties, err := fetchProperties(ctx, client)
	if err != nil {
		return "", err
	}
	filters := map[string][]any{}
	for _, pair := range pairs {
		parts := issuePropertyFilterPattern.FindStringSubmatch(pair)
		if parts == nil {
			return "", fmt.Errorf("--property %q：使用 Name=Value、Score>=3、Date<2026-01-01 或 Notes~=text", pair)
		}
		p, err := resolvePropertyRef(properties, strings.TrimSpace(parts[1]))
		if err != nil {
			return "", err
		}
		if p.Archived {
			return "", fmt.Errorf("属性 %q 已归档", p.Name)
		}
		member, err := propertyComparison(p.Type, parts[2], parts[3])
		if err != nil {
			return "", fmt.Errorf("--property %s: %w", p.Name, err)
		}
		if parts[2] == "=" && parts[3] != "__none__" {
			// 多选属性的单个 flag 对应一个元素，重复 flag 表示多个 OR 候选。
			single := p
			if single.Type == "multi_select" {
				single.Type = "select"
			}
			if single.Type == "multi_actor" {
				single.Type = "actor"
			}
			raw, err := encodeIssuePropertyValue(ctx, client, single, parts[3])
			if err != nil {
				return "", err
			}
			var v any
			if err = json.Unmarshal(raw, &v); err != nil {
				return "", err
			}
			switch value := v.(type) {
			case string:
				member = value
			case bool:
				member = strconv.FormatBool(value)
			case float64:
				member = strconv.FormatFloat(value, 'g', -1, 64)
			default:
				return "", fmt.Errorf("属性 %q 不能用标量筛选", p.Name)
			}
		}
		filters[p.ID] = append(filters[p.ID], member)
	}
	raw, err := json.Marshal(filters)
	return string(raw), err
}
