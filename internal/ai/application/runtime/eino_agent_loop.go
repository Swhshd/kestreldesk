package runtime

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ai "github.com/Swhshd/kestreldesk/internal/ai"
	"github.com/Swhshd/kestreldesk/internal/models"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"
)

const (
	einoProviderAliasPrefix       = "cap_"
	einoProviderReadableNameLimit = 35
	einoProviderHashHexLength     = 24
)

var einoReservedToolNames = map[string]struct{}{
	"tool_search":           {},
	"conversation_decision": {},
}

type einoToolAliasMap struct {
	aliasToInternal    map[string]string
	internalToProvider map[string]string
}

func newEinoToolAliasMap(definitions []ai.ToolDefinition) (*einoToolAliasMap, error) {
	return newEinoToolAliasMapWith(definitions, providerCapabilityAlias)
}

func newEinoToolAliasMapWith(definitions []ai.ToolDefinition, aliasFor func(string) string) (*einoToolAliasMap, error) {
	if aliasFor == nil {
		return nil, fmt.Errorf("provider tool alias generator is required")
	}
	aliases := &einoToolAliasMap{
		aliasToInternal:    make(map[string]string, len(definitions)),
		internalToProvider: make(map[string]string, len(definitions)),
	}
	for _, definition := range definitions {
		internalName := definition.Name
		if strings.TrimSpace(internalName) == "" {
			return nil, fmt.Errorf("Eino tool name is required")
		}
		if _, exists := aliases.internalToProvider[internalName]; exists {
			return nil, fmt.Errorf("duplicate internal Eino tool identity")
		}

		providerName := internalName
		if _, reserved := einoReservedToolNames[internalName]; !reserved {
			providerName = aliasFor(internalName)
		}
		if !isProviderSafeToolName(providerName) {
			return nil, fmt.Errorf("provider tool name violates compatibility contract")
		}
		if _, exists := aliases.aliasToInternal[providerName]; exists {
			return nil, fmt.Errorf("provider tool alias collision")
		}
		aliases.aliasToInternal[providerName] = internalName
		aliases.internalToProvider[internalName] = providerName
	}
	return aliases, nil
}

func (m *einoToolAliasMap) internalName(providerName string) (string, bool) {
	if m == nil {
		return "", false
	}
	name, ok := m.aliasToInternal[providerName]
	return name, ok
}

func (m *einoToolAliasMap) providerName(internalName string) (string, bool) {
	if m == nil {
		return "", false
	}
	name, ok := m.internalToProvider[internalName]
	return name, ok
}

func providerCapabilityAlias(internalName string) string {
	readable := strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			return char
		}
		return '_'
	}, internalName)
	readable = strings.Trim(readable, "_-")
	if readable == "" {
		readable = "tool"
	}
	if len(readable) > einoProviderReadableNameLimit {
		readable = readable[:einoProviderReadableNameLimit]
	}
	digest := sha256.Sum256([]byte(internalName))
	hash := hex.EncodeToString(digest[:])[:einoProviderHashHexLength]
	return einoProviderAliasPrefix + readable + "_" + hash
}

func isProviderSafeToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func buildEinoToolSet(definitions []ai.ToolDefinition, execute ai.ToolCallExecutor) ([]einotool.BaseTool, *einoToolAliasMap, error) {
	return buildEinoToolSetWith(definitions, execute, providerCapabilityAlias)
}

func buildEinoToolSetWith(definitions []ai.ToolDefinition, execute ai.ToolCallExecutor, aliasFor func(string) string) ([]einotool.BaseTool, *einoToolAliasMap, error) {
	if execute == nil {
		return nil, nil, fmt.Errorf("Eino tool executor is required")
	}
	aliases, err := newEinoToolAliasMapWith(definitions, aliasFor)
	if err != nil {
		return nil, nil, err
	}
	tools := make([]einotool.BaseTool, 0, len(definitions))
	for _, definition := range definitions {
		internalName := definition.Name
		providerName, ok := aliases.providerName(internalName)
		if !ok {
			return nil, nil, fmt.Errorf("provider alias is missing for Eino tool")
		}
		providerDefinition := definition
		providerDefinition.Name = providerName
		registered, err := newEinoFunctionToolWithAliases(providerDefinition, aliases, execute)
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, registered)
	}
	return tools, aliases, nil
}

// einoAgentLoop is the production model/tool loop. KestrelDesk still owns tool
// authorization, business execution, interrupts, idempotency, and auditing.
func einoAgentLoop(
	ctx context.Context,
	config models.AIConfig,
	systemPrompt string,
	userPrompt string,
	definitions []ai.ToolDefinition,
	maxSteps int,
	execute ai.ToolCallExecutor,
) (*ai.ToolLoopResult, error) {
	tools, _, err := buildEinoToolSet(definitions, execute)
	if err != nil {
		return nil, fmt.Errorf("build provider-safe Eino tools: %w", err)
	}
	model, err := newEinoChatModel(ctx, config)
	if err != nil {
		return nil, err
	}
	if maxSteps <= 0 {
		maxSteps = 6
	}
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: model,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
		MaxStep:          maxSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino agent loop: %w", err)
	}
	messages := make([]*schema.Message, 0, 2)
	if value := strings.TrimSpace(systemPrompt); value != "" {
		messages = append(messages, schema.SystemMessage(value))
	}
	messages = append(messages, schema.UserMessage(strings.TrimSpace(userPrompt)))
	result, err := agent.Generate(ctx, messages)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("Eino agent loop returned no result")
	}
	ret := &ai.ToolLoopResult{ChatCompletionResult: ai.ChatCompletionResult{
		Content:   strings.TrimSpace(result.Content),
		ModelName: config.ModelName,
	}}
	if result.ResponseMeta != nil && result.ResponseMeta.Usage != nil {
		ret.PromptTokens = result.ResponseMeta.Usage.PromptTokens
		ret.CompletionTokens = result.ResponseMeta.Usage.CompletionTokens
	}
	return ret, nil
}

func newEinoChatModel(ctx context.Context, config models.AIConfig) (einomodel.ToolCallingChatModel, error) {
	if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.ModelName) == "" {
		return nil, fmt.Errorf("ai config base URL, API key, and model name are required")
	}
	modelConfig := &einoopenai.ChatModelConfig{
		APIKey:  strings.TrimSpace(config.APIKey),
		BaseURL: strings.TrimSpace(config.BaseURL),
		Model:   strings.TrimSpace(config.ModelName),
	}
	if config.TimeoutMS > 0 {
		modelConfig.Timeout = time.Duration(config.TimeoutMS) * time.Millisecond
	}
	if config.MaxOutputTokens > 0 {
		maxTokens := config.MaxOutputTokens
		modelConfig.MaxCompletionTokens = &maxTokens
	}
	if isDashScopeQwenThinkingModel(config) {
		modelConfig.ExtraFields = map[string]any{"enable_thinking": false}
	}
	model, err := einoopenai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("create Eino OpenAI-compatible model: %w", err)
	}
	return model, nil
}

func isDashScopeQwenThinkingModel(config models.AIConfig) bool {
	baseURL := strings.ToLower(strings.TrimSpace(config.BaseURL))
	modelName := strings.ToLower(strings.TrimSpace(config.ModelName))
	return strings.Contains(baseURL, "dashscope.aliyuncs.com") && strings.HasPrefix(modelName, "qwen3")
}

type einoFunctionTool struct {
	info         *schema.ToolInfo
	providerName string
	aliases      *einoToolAliasMap
	execute      ai.ToolCallExecutor
}

var _ einotool.InvokableTool = (*einoFunctionTool)(nil)

func newEinoFunctionToolWithAliases(definition ai.ToolDefinition, aliases *einoToolAliasMap, execute ai.ToolCallExecutor) (*einoFunctionTool, error) {
	providerName := definition.Name
	if providerName == "" || execute == nil || aliases == nil {
		return nil, fmt.Errorf("Eino provider tool name, alias map, and executor are required")
	}
	if _, ok := aliases.internalName(providerName); !ok {
		return nil, fmt.Errorf("Eino provider tool name is not mapped")
	}
	info := &schema.ToolInfo{
		Name: providerName,
		Desc: strings.TrimSpace(definition.Description),
	}
	if len(definition.Parameters) > 0 {
		data, err := json.Marshal(definition.Parameters)
		if err != nil {
			return nil, fmt.Errorf("encode Eino tool schema: %w", err)
		}
		var params einojsonschema.Schema
		if err := json.Unmarshal(data, &params); err != nil {
			return nil, fmt.Errorf("decode Eino tool schema: %w", err)
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&params)
	}
	return &einoFunctionTool{info: info, providerName: providerName, aliases: aliases, execute: execute}, nil
}

func (t *einoFunctionTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *einoFunctionTool) InvokableRun(ctx context.Context, arguments string, _ ...einotool.Option) (string, error) {
	internalName, ok := t.aliases.internalName(t.providerName)
	if !ok {
		return "", fmt.Errorf("unknown provider tool alias")
	}
	result, err := t.execute(ctx, ai.ToolCall{Name: internalName, Arguments: arguments})
	if err == nil {
		return result, nil
	}
	var interrupt *agentLoopInterruptError
	if errors.As(err, &interrupt) {
		return "", err
	}
	observation, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		return "", err
	}
	return string(observation), nil
}
