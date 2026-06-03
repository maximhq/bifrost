package gigachat

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

var gigaChatFunctionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var gigaChatBuiltInFunctionNames = map[string]struct{}{
	"text2image":       {},
	"get_file_content": {},
	"text2model3d":     {},
}

const (
	gigaChatResponsesUserFunctionNamePrefix       = "__bifrost_gigachat_user_"
	gigaChatResponsesToolNameCodeInterpreter      = "code_interpreter"
	gigaChatResponsesToolNameImageGenerate        = "image_generate"
	gigaChatResponsesToolNameModel3DGenerate      = "model_3d_generate"
	gigaChatResponsesToolNameURLContentExtraction = "url_content_extraction"
	gigaChatResponsesToolNameWebSearch            = "web_search"
	gigaChatResponsesToolTypeURLContentExtraction = "url_content_extraction"
	gigaChatResponsesToolTypeModel3DGenerate      = "model_3d_generate"
	gigaChatResponsesSearchContextSizeFlagPrefix  = "search_context_size:"
	gigaChatResponsesUserLocationUserInfoField    = "user_location"
)

var gigaChatResponsesReservedFunctionNames = map[string]struct{}{
	"code_interpreter":       {},
	"image_generate":         {},
	"image_generation":       {},
	"model_3d_generate":      {},
	"url_content_extraction": {},
	"web_search":             {},
	"web_search_preview":     {},
}

// GigaChat built-ins are service-side functions with provider-specific side effects.
// They are intentionally rejected through neutral Bifrost tool fields until the provider has a scoped API for them.
func validateGigaChatFunctionName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("function tool name is required")
	}
	if _, ok := gigaChatBuiltInFunctionNames[trimmed]; ok {
		return fmt.Errorf("GigaChat built-in function %q is not supported through neutral function tools", trimmed)
	}
	if !gigaChatFunctionNamePattern.MatchString(trimmed) {
		return fmt.Errorf("function tool name %q must start with a latin letter or underscore and contain only latin letters, digits, or underscores", trimmed)
	}
	return nil
}

func validateGigaChatFunctionStrict(strict *bool) error {
	if strict != nil && *strict {
		return fmt.Errorf("function strict mode is not supported by GigaChat")
	}
	return nil
}

func toGigaChatChatFunctions(tools []schemas.ChatTool) ([]GigaChatFunction, map[string]struct{}, error) {
	if len(tools) == 0 {
		return nil, nil, nil
	}

	functions := make([]GigaChatFunction, 0, len(tools))
	functionNames := make(map[string]struct{}, len(tools))
	functionDefinitions := make(map[string]GigaChatFunction, len(tools))
	for index, tool := range tools {
		if tool.Type != schemas.ChatToolTypeFunction {
			return nil, nil, fmt.Errorf("tools[%d]: GigaChat chat completions support user-defined function tools only, got %q", index, tool.Type)
		}
		if tool.Function == nil {
			return nil, nil, fmt.Errorf("tools[%d]: function tool definition is required", index)
		}
		name := strings.TrimSpace(tool.Function.Name)
		if err := validateGigaChatFunctionName(name); err != nil {
			return nil, nil, fmt.Errorf("tools[%d]: %w", index, err)
		}
		parameters, err := sanitizeGigaChatFunctionSchema(tool.Function.Parameters)
		if err != nil {
			return nil, nil, fmt.Errorf("tools[%d]: %w", index, err)
		}
		if err := validateGigaChatFunctionStrict(tool.Function.Strict); err != nil {
			return nil, nil, fmt.Errorf("tools[%d]: %w", index, err)
		}
		if _, exists := functionNames[name]; exists {
			sameDefinition, err := sameGigaChatToolDefinition(functionDefinitions[name], GigaChatFunction{
				Name:        name,
				Description: tool.Function.Description,
				Parameters:  parameters,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("tools[%d]: compare duplicate function tool %q: %w", index, name, err)
			}
			if sameDefinition {
				continue
			}
			return nil, nil, fmt.Errorf("tools[%d]: duplicate function tool name %q has a different definition", index, name)
		}

		function := GigaChatFunction{
			Name:        name,
			Description: tool.Function.Description,
			Parameters:  parameters,
		}
		functionNames[name] = struct{}{}
		functionDefinitions[name] = function
		functions = append(functions, function)
	}

	return functions, functionNames, nil
}

type gigaChatResponsesToolsConversion struct {
	Tools    []GigaChatResponsesTool
	UserInfo map[string]interface{}
}

func toGigaChatChatFunctionCall(toolChoice *schemas.ChatToolChoice, functionNames map[string]struct{}) (interface{}, error) {
	if toolChoice == nil {
		return nil, nil
	}
	if toolChoice.ChatToolChoiceStr != nil {
		switch strings.TrimSpace(*toolChoice.ChatToolChoiceStr) {
		case "":
			return nil, nil
		case "auto":
			if len(functionNames) == 0 {
				return nil, fmt.Errorf("tool_choice auto requires at least one declared GigaChat function tool")
			}
			return "auto", nil
		case "required", "any":
			return forceSingleGigaChatChatFunctionChoice(functionNames, strings.TrimSpace(*toolChoice.ChatToolChoiceStr))
		case "none":
			return "none", nil
		default:
			return nil, fmt.Errorf("tool_choice %q is not supported by GigaChat chat completions", *toolChoice.ChatToolChoiceStr)
		}
	}
	if toolChoice.ChatToolChoiceStruct == nil {
		return nil, nil
	}

	choice := toolChoice.ChatToolChoiceStruct
	switch choice.Type {
	case schemas.ChatToolChoiceTypeFunction:
		if choice.Function == nil || strings.TrimSpace(choice.Function.Name) == "" {
			return nil, fmt.Errorf("tool_choice function name is required")
		}
		name := strings.TrimSpace(choice.Function.Name)
		if _, ok := functionNames[name]; !ok {
			return nil, fmt.Errorf("tool_choice function %q must match a declared GigaChat function tool", name)
		}
		return GigaChatFunctionCallChoice{Name: name}, nil
	case schemas.ChatToolChoiceTypeAuto:
		if len(functionNames) == 0 {
			return nil, fmt.Errorf("tool_choice auto requires at least one declared GigaChat function tool")
		}
		return "auto", nil
	case schemas.ChatToolChoiceTypeNone:
		return "none", nil
	case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
		return forceSingleGigaChatChatFunctionChoice(functionNames, string(choice.Type))
	default:
		return nil, fmt.Errorf("tool_choice type %q is not supported by GigaChat chat completions", choice.Type)
	}
}

func forceSingleGigaChatChatFunctionChoice(functionNames map[string]struct{}, choice string) (GigaChatFunctionCallChoice, error) {
	if len(functionNames) == 0 {
		return GigaChatFunctionCallChoice{}, fmt.Errorf("tool_choice %s requires at least one declared GigaChat function tool", choice)
	}
	if len(functionNames) > 1 {
		return GigaChatFunctionCallChoice{}, fmt.Errorf("tool_choice %s cannot require an arbitrary GigaChat function when multiple function tools are declared", choice)
	}
	for name := range functionNames {
		return GigaChatFunctionCallChoice{Name: name}, nil
	}
	return GigaChatFunctionCallChoice{}, fmt.Errorf("tool_choice %s requires at least one declared GigaChat function tool", choice)
}

func toGigaChatResponsesTools(tools []schemas.ResponsesTool) (*gigaChatResponsesToolsConversion, error) {
	converted := &gigaChatResponsesToolsConversion{}
	if len(tools) == 0 {
		return converted, nil
	}

	specifications := make([]GigaChatResponsesFunctionSpecification, 0, len(tools))
	functionDefinitions := make(map[string]gigaChatResponsesFunctionDefinition, len(tools))
	functionsToolIndex := -1
	for index, tool := range tools {
		switch {
		case tool.Type == schemas.ResponsesToolTypeFunction:
			specification, name, err := toGigaChatResponsesFunctionSpecification(index, tool)
			if err != nil {
				return nil, err
			}
			if existing, exists := functionDefinitions[specification.Name]; exists {
				sameDefinition, err := sameGigaChatToolDefinition(existing.Specification, *specification)
				if err != nil {
					return nil, fmt.Errorf("tools[%d]: compare duplicate function tool %q: %w", index, name, err)
				}
				if sameDefinition {
					continue
				}
				if existing.OriginalName == name {
					return nil, fmt.Errorf("tools[%d]: duplicate function tool name %q has a different definition after GigaChat compatibility remapping", index, name)
				}
				return nil, fmt.Errorf("tools[%d]: duplicate function tool name %q conflicts with %q after GigaChat compatibility remapping", index, name, existing.OriginalName)
			}
			functionDefinitions[specification.Name] = gigaChatResponsesFunctionDefinition{
				OriginalName:  name,
				Specification: *specification,
			}
			if functionsToolIndex == -1 {
				functionsToolIndex = len(converted.Tools)
				converted.Tools = append(converted.Tools, GigaChatResponsesTool{})
			}
			specifications = append(specifications, *specification)
		case isGigaChatResponsesWebSearchToolType(tool.Type):
			gigaChatTool, userInfo, err := toGigaChatResponsesWebSearchTool(index, tool)
			if err != nil {
				return nil, err
			}
			if len(userInfo) > 0 {
				if converted.UserInfo != nil {
					return nil, fmt.Errorf("tools[%d]: multiple web_search user_location configs are not supported by GigaChat Responses", index)
				}
				converted.UserInfo = userInfo
			}
			converted.Tools = append(converted.Tools, *gigaChatTool)
		case tool.Type == schemas.ResponsesToolTypeCodeInterpreter:
			gigaChatTool, err := toGigaChatResponsesCodeInterpreterTool(index, tool)
			if err != nil {
				return nil, err
			}
			converted.Tools = append(converted.Tools, *gigaChatTool)
		case tool.Type == schemas.ResponsesToolTypeImageGeneration:
			gigaChatTool, err := toGigaChatResponsesImageGenerateTool(index, tool)
			if err != nil {
				return nil, err
			}
			converted.Tools = append(converted.Tools, *gigaChatTool)
		case tool.Type == schemas.ResponsesToolTypeWebFetch || string(tool.Type) == gigaChatResponsesToolTypeURLContentExtraction:
			gigaChatTool, err := toGigaChatResponsesURLContentExtractionTool(index, tool)
			if err != nil {
				return nil, err
			}
			converted.Tools = append(converted.Tools, *gigaChatTool)
		case string(tool.Type) == gigaChatResponsesToolTypeModel3DGenerate:
			gigaChatTool, err := toGigaChatResponsesModel3DGenerateTool(index, tool)
			if err != nil {
				return nil, err
			}
			converted.Tools = append(converted.Tools, *gigaChatTool)
		default:
			return nil, fmt.Errorf("tools[%d]: GigaChat Responses does not support tool type %q", index, tool.Type)
		}
	}

	if len(specifications) > 0 {
		converted.Tools[functionsToolIndex].Functions = &GigaChatResponsesFunctionsTool{
			Specifications: specifications,
		}
	}

	return converted, nil
}

type gigaChatResponsesFunctionDefinition struct {
	OriginalName  string
	Specification GigaChatResponsesFunctionSpecification
}

func toGigaChatResponsesFunctionSpecification(index int, tool schemas.ResponsesTool) (*GigaChatResponsesFunctionSpecification, string, error) {
	if tool.Name == nil || strings.TrimSpace(*tool.Name) == "" {
		return nil, "", fmt.Errorf("tools[%d]: function tool name is required", index)
	}
	name := strings.TrimSpace(*tool.Name)
	if strings.HasPrefix(name, gigaChatResponsesUserFunctionNamePrefix) {
		return nil, "", fmt.Errorf("tools[%d]: function tool name %q uses a GigaChat compatibility-reserved prefix", index, name)
	}
	if err := validateGigaChatFunctionName(name); err != nil {
		return nil, "", fmt.Errorf("tools[%d]: %w", index, err)
	}
	if tool.ResponsesToolFunction == nil {
		return nil, "", fmt.Errorf("tools[%d]: function tool definition is required", index)
	}
	parameters, err := sanitizeGigaChatFunctionSchema(tool.ResponsesToolFunction.Parameters)
	if err != nil {
		return nil, "", fmt.Errorf("tools[%d]: %w", index, err)
	}
	if err := validateGigaChatFunctionStrict(tool.ResponsesToolFunction.Strict); err != nil {
		return nil, "", fmt.Errorf("tools[%d]: %w", index, err)
	}

	gigaChatName := toGigaChatResponsesFunctionName(name)

	return &GigaChatResponsesFunctionSpecification{
		Name:        gigaChatName,
		Description: tool.Description,
		Parameters:  parameters,
	}, name, nil
}

func sameGigaChatToolDefinition(left interface{}, right interface{}) (bool, error) {
	leftRaw, err := schemas.MarshalSorted(left)
	if err != nil {
		return false, err
	}
	rightRaw, err := schemas.MarshalSorted(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftRaw, rightRaw), nil
}

func toGigaChatResponsesFunctionName(name string) string {
	trimmed := strings.TrimSpace(name)
	if _, ok := gigaChatResponsesReservedFunctionNames[trimmed]; ok {
		return gigaChatResponsesUserFunctionNamePrefix + trimmed
	}
	return trimmed
}

func toBifrostGigaChatResponsesFunctionName(name string) string {
	trimmed := strings.TrimSpace(name)
	if !strings.HasPrefix(trimmed, gigaChatResponsesUserFunctionNamePrefix) {
		return trimmed
	}
	original := strings.TrimPrefix(trimmed, gigaChatResponsesUserFunctionNamePrefix)
	if _, ok := gigaChatResponsesReservedFunctionNames[original]; ok {
		return original
	}
	return trimmed
}

func isGigaChatResponsesWebSearchToolType(toolType schemas.ResponsesToolType) bool {
	value := strings.TrimSpace(string(toolType))
	return value == string(schemas.ResponsesToolTypeWebSearch) ||
		value == string(schemas.ResponsesToolTypeWebSearchPreview) ||
		strings.HasPrefix(value, "web_search_")
}

func toGigaChatResponsesWebSearchTool(index int, tool schemas.ResponsesTool) (*GigaChatResponsesTool, map[string]interface{}, error) {
	webSearch := &GigaChatResponsesWebSearchTool{}
	var userInfo map[string]interface{}

	if tool.ResponsesToolWebSearch != nil {
		if tool.ResponsesToolWebSearch.Filters != nil {
			return nil, nil, fmt.Errorf("tools[%d]: web_search filters are not supported by GigaChat Responses", index)
		}
		if len(tool.ResponsesToolWebSearch.SearchContentTypes) > 0 {
			return nil, nil, fmt.Errorf("tools[%d]: web_search search_content_types are not supported by GigaChat Responses", index)
		}
		if tool.ResponsesToolWebSearch.ExternalWebAccess != nil {
			return nil, nil, fmt.Errorf("tools[%d]: web_search external_web_access is not supported by GigaChat Responses", index)
		}
		if tool.ResponsesToolWebSearch.MaxUses != nil {
			return nil, nil, fmt.Errorf("tools[%d]: web_search max_uses is not supported by GigaChat Responses", index)
		}
		if tool.ResponsesToolWebSearch.SearchContextSize != nil && strings.TrimSpace(*tool.ResponsesToolWebSearch.SearchContextSize) != "" {
			webSearch.Flags = append(webSearch.Flags, gigaChatResponsesSearchContextSizeFlagPrefix+strings.TrimSpace(*tool.ResponsesToolWebSearch.SearchContextSize))
		}
		if tool.ResponsesToolWebSearch.UserLocation != nil {
			userInfo = toGigaChatResponsesUserInfo(tool.ResponsesToolWebSearch.UserLocation)
		}
	}
	if tool.ResponsesToolWebSearchPreview != nil {
		if tool.ResponsesToolWebSearchPreview.SearchContextSize != nil && strings.TrimSpace(*tool.ResponsesToolWebSearchPreview.SearchContextSize) != "" {
			webSearch.Flags = append(webSearch.Flags, gigaChatResponsesSearchContextSizeFlagPrefix+strings.TrimSpace(*tool.ResponsesToolWebSearchPreview.SearchContextSize))
		}
		if tool.ResponsesToolWebSearchPreview.UserLocation != nil {
			userInfo = toGigaChatResponsesUserInfo(tool.ResponsesToolWebSearchPreview.UserLocation)
		}
	}

	return &GigaChatResponsesTool{WebSearch: webSearch}, userInfo, nil
}

func toGigaChatResponsesUserInfo(location *schemas.ResponsesToolWebSearchUserLocation) map[string]interface{} {
	if location == nil {
		return nil
	}
	userLocation := make(map[string]interface{})
	if location.Type != nil && strings.TrimSpace(*location.Type) != "" {
		userLocation["type"] = strings.TrimSpace(*location.Type)
	}
	if location.City != nil && strings.TrimSpace(*location.City) != "" {
		userLocation["city"] = strings.TrimSpace(*location.City)
	}
	if location.Country != nil && strings.TrimSpace(*location.Country) != "" {
		userLocation["country"] = strings.TrimSpace(*location.Country)
	}
	if location.Region != nil && strings.TrimSpace(*location.Region) != "" {
		userLocation["region"] = strings.TrimSpace(*location.Region)
	}
	if location.Timezone != nil && strings.TrimSpace(*location.Timezone) != "" {
		userLocation["timezone"] = strings.TrimSpace(*location.Timezone)
	}
	if len(userLocation) == 0 {
		return nil
	}
	return map[string]interface{}{gigaChatResponsesUserLocationUserInfoField: userLocation}
}

func toGigaChatResponsesCodeInterpreterTool(index int, tool schemas.ResponsesTool) (*GigaChatResponsesTool, error) {
	config, err := toGigaChatResponsesToolConfigMap(tool.ResponsesToolCodeInterpreter)
	if err != nil {
		return nil, fmt.Errorf("tools[%d]: code_interpreter config is invalid: %w", index, err)
	}
	return &GigaChatResponsesTool{CodeInterpreter: config}, nil
}

func toGigaChatResponsesImageGenerateTool(index int, tool schemas.ResponsesTool) (*GigaChatResponsesTool, error) {
	config, err := toGigaChatResponsesToolConfigMap(tool.ResponsesToolImageGeneration)
	if err != nil {
		return nil, fmt.Errorf("tools[%d]: image_generation config is invalid: %w", index, err)
	}
	return &GigaChatResponsesTool{ImageGenerate: config}, nil
}

func toGigaChatResponsesURLContentExtractionTool(index int, tool schemas.ResponsesTool) (*GigaChatResponsesTool, error) {
	config, err := toGigaChatResponsesToolConfigMap(tool.ResponsesToolWebFetch)
	if err != nil {
		return nil, fmt.Errorf("tools[%d]: url_content_extraction config is invalid: %w", index, err)
	}
	addGigaChatResponsesCommonToolFields(config, tool)
	return &GigaChatResponsesTool{URLContentExtraction: config}, nil
}

func toGigaChatResponsesModel3DGenerateTool(index int, tool schemas.ResponsesTool) (*GigaChatResponsesTool, error) {
	config, err := toGigaChatResponsesToolConfigMap(nil)
	if err != nil {
		return nil, fmt.Errorf("tools[%d]: model_3d_generate config is invalid: %w", index, err)
	}
	addGigaChatResponsesCommonToolFields(config, tool)
	return &GigaChatResponsesTool{Model3DGenerate: config}, nil
}

func toGigaChatResponsesToolConfigMap(value interface{}) (map[string]interface{}, error) {
	config := map[string]interface{}{}
	if value == nil {
		return config, nil
	}

	raw, err := schemas.MarshalSorted(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]interface{}
	if err := schemas.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for name, fieldValue := range fields {
		if fieldValue != nil {
			config[name] = fieldValue
		}
	}
	return config, nil
}

func addGigaChatResponsesCommonToolFields(config map[string]interface{}, tool schemas.ResponsesTool) {
	if config == nil {
		return
	}
	if tool.Name != nil && strings.TrimSpace(*tool.Name) != "" {
		config["name"] = strings.TrimSpace(*tool.Name)
	}
	if tool.Description != nil && strings.TrimSpace(*tool.Description) != "" {
		config["description"] = strings.TrimSpace(*tool.Description)
	}
}

func toGigaChatResponsesToolConfig(toolChoice *schemas.ResponsesToolChoice, tools []schemas.ResponsesTool) (*GigaChatResponsesToolConfig, error) {
	if toolChoice == nil {
		return nil, nil
	}
	targets := newGigaChatResponsesToolChoiceTargets(tools)
	if toolChoice.ResponsesToolChoiceStr != nil {
		switch strings.TrimSpace(*toolChoice.ResponsesToolChoiceStr) {
		case "":
			return nil, nil
		case "auto":
			if !targets.HasTools() {
				return nil, fmt.Errorf("tool_choice auto requires at least one declared GigaChat tool")
			}
			return &GigaChatResponsesToolConfig{Mode: "auto"}, nil
		case "none":
			return &GigaChatResponsesToolConfig{Mode: "none"}, nil
		case "required", "any":
			return forceSingleGigaChatResponsesToolConfig(targets, strings.TrimSpace(*toolChoice.ResponsesToolChoiceStr))
		default:
			return nil, fmt.Errorf("tool_choice %q is not supported by GigaChat Responses", *toolChoice.ResponsesToolChoiceStr)
		}
	}
	if toolChoice.ResponsesToolChoiceStruct == nil {
		return nil, nil
	}

	choice := toolChoice.ResponsesToolChoiceStruct
	switch choice.Type {
	case schemas.ResponsesToolChoiceTypeFunction:
		if choice.Name == nil || strings.TrimSpace(*choice.Name) == "" {
			return nil, fmt.Errorf("tool_choice function name is required")
		}
		name := strings.TrimSpace(*choice.Name)
		gigaChatName, ok := targets.Functions[name]
		if !ok {
			return nil, fmt.Errorf("tool_choice function %q must match a declared GigaChat function tool", name)
		}
		return &GigaChatResponsesToolConfig{
			Mode:         "forced",
			FunctionName: &gigaChatName,
		}, nil
	case schemas.ResponsesToolChoiceTypeAuto:
		if !targets.HasTools() {
			return nil, fmt.Errorf("tool_choice auto requires at least one declared GigaChat tool")
		}
		return &GigaChatResponsesToolConfig{Mode: "auto"}, nil
	case schemas.ResponsesToolChoiceTypeNone:
		return &GigaChatResponsesToolConfig{Mode: "none"}, nil
	case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
		return forceSingleGigaChatResponsesToolConfig(targets, string(choice.Type))
	case schemas.ResponsesToolChoiceTypeAllowedTools:
		return nil, fmt.Errorf("tool_choice type %q is not supported by GigaChat Responses because tool_config supports one forced tool_name or function_name, not an allowed tools set", choice.Type)
	case schemas.ResponsesToolChoiceTypeFileSearch, schemas.ResponsesToolChoiceTypeComputerUsePreview, schemas.ResponsesToolChoiceTypeMCP, schemas.ResponsesToolChoiceTypeCustom:
		return nil, fmt.Errorf("tool_choice type %q is not supported by GigaChat Responses", choice.Type)
	default:
		toolName, ok := targets.BuiltIns[gigaChatResponsesToolChoiceTypeToBuiltInName(choice.Type)]
		if !ok {
			return nil, fmt.Errorf("tool_choice type %q must match a declared GigaChat built-in tool", choice.Type)
		}
		return &GigaChatResponsesToolConfig{
			Mode:     "forced",
			ToolName: &toolName,
		}, nil
	}
}

func forceSingleGigaChatResponsesToolConfig(targets gigaChatResponsesToolChoiceTargets, choice string) (*GigaChatResponsesToolConfig, error) {
	targetCount := len(targets.Functions) + len(targets.BuiltIns)
	if targetCount == 0 {
		return nil, fmt.Errorf("tool_choice %s requires at least one declared GigaChat tool", choice)
	}
	if targetCount > 1 {
		return nil, fmt.Errorf("tool_choice %s cannot require an arbitrary GigaChat tool when multiple tools are declared", choice)
	}
	for _, name := range targets.Functions {
		return &GigaChatResponsesToolConfig{
			Mode:         "forced",
			FunctionName: &name,
		}, nil
	}
	for _, name := range targets.BuiltIns {
		return &GigaChatResponsesToolConfig{
			Mode:     "forced",
			ToolName: &name,
		}, nil
	}
	return nil, fmt.Errorf("tool_choice %s requires at least one declared GigaChat tool", choice)
}

type gigaChatResponsesToolChoiceTargets struct {
	Functions map[string]string
	BuiltIns  map[string]string
}

func newGigaChatResponsesToolChoiceTargets(tools []schemas.ResponsesTool) gigaChatResponsesToolChoiceTargets {
	targets := gigaChatResponsesToolChoiceTargets{
		Functions: make(map[string]string, len(tools)),
		BuiltIns:  make(map[string]string, len(tools)),
	}

	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeFunction && tool.Name != nil {
			name := strings.TrimSpace(*tool.Name)
			if name != "" {
				targets.Functions[name] = toGigaChatResponsesFunctionName(name)
			}
			continue
		}
		if toolName, ok := gigaChatResponsesToolTypeToBuiltInName(tool.Type); ok {
			targets.BuiltIns[toolName] = toolName
		}
	}
	return targets
}

func (targets gigaChatResponsesToolChoiceTargets) HasTools() bool {
	return len(targets.Functions) > 0 || len(targets.BuiltIns) > 0
}

func gigaChatResponsesToolTypeToBuiltInName(toolType schemas.ResponsesToolType) (string, bool) {
	switch {
	case isGigaChatResponsesWebSearchToolType(toolType):
		return gigaChatResponsesToolNameWebSearch, true
	case toolType == schemas.ResponsesToolTypeCodeInterpreter:
		return gigaChatResponsesToolNameCodeInterpreter, true
	case toolType == schemas.ResponsesToolTypeImageGeneration:
		return gigaChatResponsesToolNameImageGenerate, true
	case toolType == schemas.ResponsesToolTypeWebFetch || string(toolType) == gigaChatResponsesToolTypeURLContentExtraction:
		return gigaChatResponsesToolNameURLContentExtraction, true
	case string(toolType) == gigaChatResponsesToolTypeModel3DGenerate:
		return gigaChatResponsesToolNameModel3DGenerate, true
	default:
		return "", false
	}
}

func gigaChatResponsesToolChoiceTypeToBuiltInName(choiceType schemas.ResponsesToolChoiceType) string {
	value := strings.TrimSpace(string(choiceType))
	switch {
	case value == string(schemas.ResponsesToolChoiceTypeCodeInterpreter):
		return gigaChatResponsesToolNameCodeInterpreter
	case value == string(schemas.ResponsesToolChoiceTypeImageGeneration):
		return gigaChatResponsesToolNameImageGenerate
	case value == string(schemas.ResponsesToolChoiceTypeWebSearchPreview) ||
		value == string(schemas.ResponsesToolTypeWebSearch) ||
		strings.HasPrefix(value, string(schemas.ResponsesToolTypeWebSearch)+"_"):
		return gigaChatResponsesToolNameWebSearch
	case value == string(schemas.ResponsesToolTypeWebFetch) || value == gigaChatResponsesToolTypeURLContentExtraction:
		return gigaChatResponsesToolNameURLContentExtraction
	case value == gigaChatResponsesToolTypeModel3DGenerate:
		return gigaChatResponsesToolNameModel3DGenerate
	default:
		return ""
	}
}

func unsupportedGigaChatToolControlExtraParams(extraParams map[string]interface{}, names ...string) []string {
	if len(extraParams) == 0 {
		return nil
	}

	unsupported := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := extraParams[name]; ok {
			unsupported = append(unsupported, "extra_params."+name)
		}
	}
	sort.Strings(unsupported)
	return unsupported
}
