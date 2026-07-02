package agent

import "strings"

func acpToolNameFromTitle(title string, mapTodoList bool) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}

	// ACP titles often look like "Tool Name: argument detail".
	if idx := strings.Index(t, ":"); idx > 0 {
		t = strings.TrimSpace(t[:idx])
	}

	lower := strings.ToLower(t)
	switch lower {
	case "read", "read file":
		return "read_file"
	case "write", "write file":
		return "write_file"
	case "edit", "patch":
		return "edit_file"
	case "shell", "bash", "terminal", "run command", "run shell command":
		return "terminal"
	case "search", "grep", "find":
		return "search_files"
	case "glob":
		return "glob"
	case "code":
		return "code"
	case "web search":
		return "web_search"
	case "fetch", "web fetch":
		return "web_fetch"
	case "todo", "todo write":
		return "todo_write"
	case "todo list", "todo_list":
		if mapTodoList {
			return "todo_write"
		}
	}

	return strings.ReplaceAll(lower, " ", "_")
}
