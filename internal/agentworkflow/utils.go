package agentworkflow

import (
	"encoding/json"
	"log"
)

// mustMarshalJSON 将任何值序列化为JSON字符串
func mustMarshalJSON(v interface{}) string {
	if v == nil {
		return ""
	}

	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[AgentWorkflow] JSON序列化失败: %v", err)
		return ""
	}
	return string(data)
}

// mustUnmarshalJSON 将JSON字符串反序列化为指定类型
func mustUnmarshalJSON(jsonStr string, v interface{}) error {
	if jsonStr == "" {
		return nil
	}
	return json.Unmarshal([]byte(jsonStr), v)
}
