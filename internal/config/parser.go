package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type yamlLine struct {
	indent  int
	content string
}

func parseYAML(r io.Reader) (map[string]interface{}, error) {
	lines, err := readYAMLLines(r)
	if err != nil {
		return nil, err
	}
	result, idx, err := parseMap(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if idx != len(lines) {
		return nil, fmt.Errorf("存在无法解析的 YAML 内容 (第 %d 行)", idx+1)
	}
	return result, nil
}

func readYAMLLines(r io.Reader) ([]yamlLine, error) {
	scanner := bufio.NewScanner(r)
	var lines []yamlLine
	lineNum := 0
	for scanner.Scan() {
		raw := scanner.Text()
		lineNum++
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent%2 != 0 {
			return nil, fmt.Errorf("YAML 仅支持 2 个空格缩进 (第 %d 行)", lineNum)
		}
		lines = append(lines, yamlLine{indent: indent, content: strings.TrimSpace(raw)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func parseMap(lines []yamlLine, idx int, indent int) (map[string]interface{}, int, error) {
	result := map[string]interface{}{}
	for idx < len(lines) {
		line := lines[idx]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, idx, fmt.Errorf("缩进错误 (第 %d 行)", idx+1)
		}
		if strings.HasPrefix(strings.TrimSpace(line.content), "- ") {
			return nil, idx, fmt.Errorf("序列条目不能直接出现在映射层级 (第 %d 行)", idx+1)
		}
		parts := strings.SplitN(line.content, ":", 2)
		if len(parts) == 0 {
			return nil, idx, fmt.Errorf("缺少 ':' (第 %d 行)", idx+1)
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			return nil, idx, fmt.Errorf("key 为空 (第 %d 行)", idx+1)
		}
		var value interface{}
		if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
			idx++
			if idx >= len(lines) || lines[idx].indent <= indent {
				result[key] = map[string]interface{}{}
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(lines[idx].content), "- ") {
				list, nextIdx, err := parseList(lines, idx, indent+2)
				if err != nil {
					return nil, idx, err
				}
				value = list
				idx = nextIdx
			} else {
				child, nextIdx, err := parseMap(lines, idx, indent+2)
				if err != nil {
					return nil, idx, err
				}
				value = child
				idx = nextIdx
			}
		} else {
			value = parseScalar(strings.TrimSpace(parts[1]))
			idx++
		}
		if _, exists := result[key]; exists {
			return nil, idx, fmt.Errorf("重复的 key '%s'", key)
		}
		result[key] = value
	}
	return result, idx, nil
}

func parseList(lines []yamlLine, idx int, indent int) ([]interface{}, int, error) {
	var list []interface{}
	for idx < len(lines) {
		line := lines[idx]
		if line.indent < indent-2 {
			break
		}
		if line.indent < indent {
			break
		}
		trimmed := strings.TrimSpace(line.content)
		if !strings.HasPrefix(trimmed, "- ") {
			break
		}
		item := strings.TrimSpace(trimmed[2:])
		idx++
		if item != "" {
			if obj, ok := parseInlineMap(item); ok {
				// merge nested map values if present
				if idx < len(lines) && lines[idx].indent >= indent+2 && !strings.HasPrefix(strings.TrimSpace(lines[idx].content), "- ") {
					child, nextIdx, err := parseMap(lines, idx, indent+2)
					if err != nil {
						return nil, idx, err
					}
					for k, v := range child {
						obj[k] = v
					}
					idx = nextIdx
				}
				list = append(list, obj)
				continue
			}
			list = append(list, parseScalar(item))
			continue
		}
		if idx >= len(lines) {
			list = append(list, map[string]interface{}{})
			break
		}
		if strings.HasPrefix(strings.TrimSpace(lines[idx].content), "- ") && lines[idx].indent >= indent {
			subList, nextIdx, err := parseList(lines, idx, indent+2)
			if err != nil {
				return nil, idx, err
			}
			list = append(list, subList)
			idx = nextIdx
			continue
		}
		child, nextIdx, err := parseMap(lines, idx, indent+2)
		if err != nil {
			return nil, idx, err
		}
		list = append(list, child)
		idx = nextIdx
	}
	return list, idx, nil
}

func parseScalar(value string) interface{} {
	// Try to unquote first so quoted booleans/numbers are handled.
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
	} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		value = strings.Trim(value, "'")
	}

	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	switch lower {
	case "", "null", "~":
		return nil
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.ParseInt(value, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return f
	}
	return value
}

func parseInlineMap(item string) (map[string]interface{}, bool) {
	if !strings.Contains(item, ":") {
		return nil, false
	}
	parts := strings.SplitN(item, ":", 2)
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return nil, false
	}
	value := strings.TrimSpace(parts[1])
	return map[string]interface{}{key: parseScalar(value)}, true
}
