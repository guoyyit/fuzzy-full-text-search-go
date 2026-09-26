package model

// Document 是被索引的文档：ID 标识加若干文本字段。
type Document struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}
