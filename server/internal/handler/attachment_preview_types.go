package handler

import (
	_ "embed"
	"encoding/json"
)

//go:embed attachment_preview_types.json
var attachmentPreviewTypesJSON []byte

type attachmentPreviewTypesFile struct {
	TextExtensions   []string `json:"text_extensions"`
	TextContentTypes []string `json:"text_content_types"`
	TextBasenames    []string `json:"text_basenames"`
}

type attachmentPreviewTypes struct {
	textExtensions   map[string]struct{}
	textContentTypes map[string]struct{}
	textBasenames    map[string]struct{}
}

var textAttachmentPreviewTypes = loadAttachmentPreviewTypes()

func loadAttachmentPreviewTypes() attachmentPreviewTypes {
	var data attachmentPreviewTypesFile
	if err := json.Unmarshal(attachmentPreviewTypesJSON, &data); err != nil {
		panic("handler: parse attachment_preview_types.json: " + err.Error())
	}
	return attachmentPreviewTypes{
		textExtensions:   uniquePreviewValues("text_extensions", data.TextExtensions),
		textContentTypes: uniquePreviewValues("text_content_types", data.TextContentTypes),
		textBasenames:    uniquePreviewValues("text_basenames", data.TextBasenames),
	}
}

func uniquePreviewValues(name string, values []string) map[string]struct{} {
	if len(values) == 0 {
		panic("handler: attachment preview " + name + " cannot be empty")
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			panic("handler: attachment preview " + name + " contains an empty value")
		}
		if _, duplicate := result[value]; duplicate {
			panic("handler: attachment preview " + name + " contains duplicate " + value)
		}
		result[value] = struct{}{}
	}
	return result
}
