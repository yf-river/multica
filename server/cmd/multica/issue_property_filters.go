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

func buildIssuePropertyFiltersWithCatalog(ctx context.Context, client *cli.APIClient, directory *memberDirectory, properties []propertyDTO, pairs []string) (string, error) {
	filters := map[string][]any{}
	for _, pair := range pairs {
		parts := issuePropertyFilterPattern.FindStringSubmatch(pair)
		if parts == nil {
			return "", fmt.Errorf("--property %q：使用 Name=Value、Score>=3、Date<2026-01-01 或 Notes~=text", pair)
		}
		if strings.TrimSpace(parts[3]) == "" {
			return "", fmt.Errorf("--property: value cannot be empty; use __none__ to match unset properties")
		}
		p, err := resolvePropertyRef(properties, strings.TrimSpace(parts[1]))
		if err != nil {
			return "", err
		}
		if p.Archived {
			return "", fmt.Errorf("属性 %q 已归档", p.Name)
		}
		var member any
		if parts[2] != "=" {
			member, err = propertyComparison(p.Type, parts[2], parts[3])
			if err != nil {
				return "", fmt.Errorf("--property %s: %w", p.Name, err)
			}
		}
		if parts[2] == "=" {
			member, err = resolvePropertyFilterValue(ctx, client, directory, p, parts[3])
			if err != nil {
				return "", err
			}
		}
		duplicate := false
		encoded, _ := json.Marshal(member)
		for _, current := range filters[p.ID] {
			existing, _ := json.Marshal(current)
			if string(encoded) == string(existing) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}

		filters[p.ID] = append(filters[p.ID], member)
	}
	raw, err := json.Marshal(filters)
	return string(raw), err
}
