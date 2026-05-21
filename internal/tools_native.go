package internal

// ConvertToolMessages 转换 OpenAI 格式的 tool_calls/tool 消息为 z.ai 能理解的格式
// 原生 function calling 模式下，z.ai 应该能直接处理这些消息
func ConvertToolMessages(messages []Message) []Message {
	// z.ai 原生 function calling 应该能直接处理 OpenAI 格式的消息
	// 不需要转换，直接返回
	return messages
}
