package tools

import (
	"fmt"
	"strings"
)

// GetModelInfo returns detailed specs for a specific model.
func GetModelInfo(modelID string) string {
	if modelID == "" {
		return "Please provide a model ID. Example: `get_model_info(model_id=\"gpt-5\")`"
	}
	m, found := FindModel(modelID)
	if !found {
		suggestions := SuggestModels(modelID, 3)
		return fmt.Sprintf("Model `%s` not found in registry. Did you mean: %s",
			modelID, strings.Join(suggestions, ", "))
	}
	return ModelDetail(m)
}
