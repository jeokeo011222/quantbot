package util

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ExtractJSON 从LLM输出中提取干净的JSON结果
// LLM经常会在JSON前后添加文字说明，此函数会：
// 1. 优先使用正则匹配首个完整的JSON对象
// 2. 尝试直接解析
// 3. 如果都失败，返回清理后的原文
func ExtractJSON(raw string) string {
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return "{}"
	}

	// 直接尝试解析整个字符串
	if json.Valid([]byte(raw)) {
		return raw
	}

	// 移除 markdown 代码块标记
	cleaned := raw
	cleaned = strings.ReplaceAll(cleaned, "```json", "")
	cleaned = strings.ReplaceAll(cleaned, "```JSON", "")
	cleaned = strings.ReplaceAll(cleaned, "```", "")
	cleaned = strings.TrimSpace(cleaned)

	if json.Valid([]byte(cleaned)) {
		return cleaned
	}

	// 尝试提取第一个 { 到最后一个 } 的内容
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		if lastIdx := strings.LastIndex(cleaned, "}"); lastIdx > idx {
			candidate := cleaned[idx : lastIdx+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}

	// 尝试用正则匹配 JSON 对象
	re := regexp.MustCompile(`\{[\s\S]*\}`)
	matches := re.FindAllString(cleaned, -1)
	for _, m := range matches {
		if json.Valid([]byte(m)) {
			return m
		}
	}

	// 尝试提取 JSON 数组
	reArr := regexp.MustCompile(`\[[\s\S]*\]`)
	arrMatches := reArr.FindAllString(cleaned, -1)
	for _, m := range arrMatches {
		if json.Valid([]byte(m)) {
			return m
		}
	}

	// 兜底：返回清理后的原文
	return cleaned
}

// ParseJSONResponse 解析LLM响应为map
func ParseJSONResponse(raw string) (map[string]interface{}, error) {
	extracted := ExtractJSON(raw)
	var result map[string]interface{}
	err := json.Unmarshal([]byte(extracted), &result)
	return result, err
}

// IsValidJSON 检查字符串是否为有效JSON
func IsValidJSON(s string) bool {
	return json.Valid([]byte(strings.TrimSpace(s)))
}
