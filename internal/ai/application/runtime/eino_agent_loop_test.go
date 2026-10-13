package runtime

import (
	"context"
	"strings"
	"testing"

	ai "github.com/Swhshd/kestreldesk/internal/ai"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/toolx"
	svc "github.com/Swhshd/kestreldesk/internal/services"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestEinoToolSetRegistersProviderAliasAndDispatchesOriginalIdentity(t *testing.T) {
	const internalID = "builtin/conversation_context"
	const arguments = "{\"request\":\"inspect current conversation\"}"

	definitions := []ai.ToolDefinition{
		agentLoopToolSearchTool,
		{
			Name:        internalID,
			Description: "Inspect the current conversation.",
			Parameters:  map[string]any{"type": "object", "additionalProperties": true},
		},
		agentLoopDecisionTool,
	}
	var dispatched ai.ToolCall
	tools, aliases, err := buildEinoToolSet(definitions, func(_ context.Context, call ai.ToolCall) (string, error) {
		dispatched = call
		return "conversation context result", nil
	})
	if err != nil {
		t.Fatalf("register Eino tool set: %v", err)
	}

	providerName, ok := aliases.providerName(internalID)
	if !ok {
		t.Fatalf("provider alias missing for %q", internalID)
	}
	if providerName == internalID {
		t.Fatalf("internal capability ID %q was exposed as a provider name", internalID)
	}
	seen := make(map[string]bool, len(tools))
	for _, registered := range tools {
		info, err := registered.Info(context.Background())
		if err != nil {
			t.Fatalf("read registered ToolInfo: %v", err)
		}
		if !isProviderSafeToolName(info.Name) {
			t.Errorf("model-visible ToolInfo name %q violates provider function-name contract", info.Name)
		}
		if seen[info.Name] {
			t.Errorf("duplicate model-visible ToolInfo name %q", info.Name)
		}
		seen[info.Name] = true
	}
	if !seen["tool_search"] || !seen["conversation_decision"] {
		t.Fatalf("reserved names missing from registered tool set: %#v", seen)
	}

	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools})
	if err != nil {
		t.Fatalf("create Eino ToolNode: %v", err)
	}
	results, err := node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-1", Function: schema.FunctionCall{Name: providerName, Arguments: arguments},
	}}))
	if err != nil {
		t.Fatalf("dispatch provider alias through Eino ToolNode: %v", err)
	}
	if len(results) != 1 || results[0].Content != "conversation context result" {
		t.Fatalf("unexpected Eino tool result: %#v", results)
	}
	if dispatched.Name != internalID {
		t.Fatalf("executor received provider name %q; want original internal ID %q", dispatched.Name, internalID)
	}
	if dispatched.Arguments != arguments {
		t.Fatalf("executor arguments=%q; want unchanged %q", dispatched.Arguments, arguments)
	}
}

func TestProviderToolAliasesMeetNameContractAndRemainDeterministic(t *testing.T) {
	internalIDs := []string{
		"crm/update_customer",
		"crm_update_customer",
		"skill/77",
		"already-valid_identifier-1",
		strings.Repeat("workflow/very-long-capability-", 8),
	}
	definitions := []ai.ToolDefinition{agentLoopToolSearchTool, agentLoopDecisionTool}
	for _, internalID := range internalIDs {
		definitions = append(definitions, ai.ToolDefinition{Name: internalID, Description: "test capability"})
	}

	firstTools, firstAliases, err := buildEinoToolSet(definitions, func(context.Context, ai.ToolCall) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("build first Eino tool set: %v", err)
	}
	secondTools, secondAliases, err := buildEinoToolSet(definitions, func(context.Context, ai.ToolCall) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("build second Eino tool set: %v", err)
	}
	if len(firstTools) != len(secondTools) {
		t.Fatalf("tool set size changed: %d != %d", len(firstTools), len(secondTools))
	}

	seen := make(map[string]bool, len(firstTools))
	for i := range firstTools {
		firstInfo, err := firstTools[i].Info(context.Background())
		if err != nil {
			t.Fatalf("read first ToolInfo: %v", err)
		}
		secondInfo, err := secondTools[i].Info(context.Background())
		if err != nil {
			t.Fatalf("read second ToolInfo: %v", err)
		}
		if !isProviderSafeToolName(firstInfo.Name) {
			t.Errorf("provider name %q is empty, invalid, or longer than 64 characters", firstInfo.Name)
		}
		if seen[firstInfo.Name] {
			t.Errorf("provider tool set contains duplicate name %q", firstInfo.Name)
		}
		seen[firstInfo.Name] = true
		if firstInfo.Name != secondInfo.Name {
			t.Errorf("provider alias changed between runs: %q != %q", firstInfo.Name, secondInfo.Name)
		}
	}

	for _, internalID := range internalIDs {
		alias, ok := firstAliases.providerName(internalID)
		if !ok {
			t.Errorf("provider alias missing for %q", internalID)
			continue
		}
		if alias == internalID {
			t.Errorf("non-reserved identity %q was passed through without aliasing", internalID)
		}
	}
	firstSlashAlias, _ := firstAliases.providerName("crm/update_customer")
	secondSlashAlias, _ := firstAliases.providerName("crm_update_customer")
	if firstSlashAlias == secondSlashAlias {
		t.Fatalf("sanitize-colliding internal IDs received the same alias %q", firstSlashAlias)
	}
	if mapped, ok := secondAliases.internalName(firstSlashAlias); !ok || mapped != "crm/update_customer" {
		t.Fatalf("alias did not map back to its exact original identity: got %q, found=%t", mapped, ok)
	}
	longAlias, _ := firstAliases.providerName(internalIDs[len(internalIDs)-1])
	if len(longAlias) > 64 {
		t.Fatalf("long internal ID produced %d-character alias", len(longAlias))
	}
	for _, reserved := range []string{"tool_search", "conversation_decision"} {
		if !seen[reserved] {
			t.Errorf("reserved function name %q was not included in final uniqueness check", reserved)
		}
	}
}

func TestEinoToolAliasMapFailsClosedForUnknownAlias(t *testing.T) {
	aliases, err := newEinoToolAliasMap([]ai.ToolDefinition{
		agentLoopToolSearchTool,
		agentLoopDecisionTool,
		{Name: "builtin/conversation_context"},
	})
	if err != nil {
		t.Fatalf("build alias map: %v", err)
	}
	if _, ok := aliases.internalName("unknown_provider_alias"); ok {
		t.Fatal("unknown provider alias resolved to an internal capability")
	}
}

func TestEinoToolAliasPreservesCompleteUntrimmedIdentity(t *testing.T) {
	internalIDs := []string{" builtin/conversation_context", "builtin/conversation_context "}
	definitions := []ai.ToolDefinition{{Name: internalIDs[0]}, {Name: internalIDs[1]}}
	var dispatched ai.ToolCall
	tools, aliases, err := buildEinoToolSet(definitions, func(_ context.Context, call ai.ToolCall) (string, error) {
		dispatched = call
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("build tool set with distinct complete identities: %v", err)
	}
	firstAlias, ok := aliases.providerName(internalIDs[0])
	if !ok {
		t.Fatalf("missing alias for identity %q", internalIDs[0])
	}
	secondAlias, ok := aliases.providerName(internalIDs[1])
	if !ok {
		t.Fatalf("missing alias for identity %q", internalIDs[1])
	}
	if firstAlias == secondAlias {
		t.Fatalf("distinct complete identities received the same provider alias %q", firstAlias)
	}
	if mapped, ok := aliases.internalName(firstAlias); !ok || mapped != internalIDs[0] {
		t.Fatalf("first alias mapped to %q, found=%t; want complete identity %q", mapped, ok, internalIDs[0])
	}

	var firstTool einotool.BaseTool
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("read ToolInfo: %v", err)
		}
		if info.Name == firstAlias {
			firstTool = tool
			break
		}
	}
	if firstTool == nil {
		t.Fatalf("registered Eino tool for alias %q not found", firstAlias)
	}
	if _, err := firstTool.(einotool.InvokableTool).InvokableRun(context.Background(), "{}"); err != nil {
		t.Fatalf("invoke tool with untrimmed internal identity: %v", err)
	}
	if dispatched.Name != internalIDs[0] {
		t.Fatalf("executor received identity %q; want complete original %q", dispatched.Name, internalIDs[0])
	}
	if _, err := newEinoToolAliasMap([]ai.ToolDefinition{{Name: "  "}}); err == nil {
		t.Fatal("blank internal identity must still be rejected")
	}
}

func TestEinoToolAliasMapRejectsDuplicateAliases(t *testing.T) {
	definitions := []ai.ToolDefinition{
		agentLoopToolSearchTool,
		agentLoopDecisionTool,
		{Name: "crm/get_customer"},
		{Name: "crm/update_customer"},
	}
	_, err := newEinoToolAliasMapWith(definitions, func(string) string { return "same_alias" })
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("duplicate provider alias must fail fast with a collision error, got %v", err)
	}
}

func TestEinoToolAliasMapRejectsReservedNameCollisions(t *testing.T) {
	for _, reserved := range []string{"tool_search", "conversation_decision"} {
		t.Run(reserved, func(t *testing.T) {
			definitions := []ai.ToolDefinition{
				agentLoopToolSearchTool,
				agentLoopDecisionTool,
				{Name: "mcp/server_tool"},
			}
			_, err := newEinoToolAliasMapWith(definitions, func(string) string { return reserved })
			if err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatalf("alias collision with reserved name %q must fail fast, got %v", reserved, err)
			}
		})
	}
}

func TestEinoToolNodeFailsClosedForUnknownProviderAlias(t *testing.T) {
	called := false
	tools, _, err := buildEinoToolSet([]ai.ToolDefinition{
		agentLoopToolSearchTool,
		{Name: "builtin/conversation_context"},
		agentLoopDecisionTool,
	}, func(context.Context, ai.ToolCall) (string, error) {
		called = true
		return "must not execute", nil
	})
	if err != nil {
		t.Fatalf("register Eino tool set: %v", err)
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools})
	if err != nil {
		t.Fatalf("create Eino ToolNode: %v", err)
	}
	_, err = node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-unknown", Function: schema.FunctionCall{Name: "unknown_provider_alias", Arguments: "{}"},
	}}))
	if err == nil {
		t.Fatal("unknown provider alias must fail instead of falling back to an internal capability")
	}
	if called {
		t.Fatal("unknown provider alias reached the executor")
	}
}

func TestEinoProviderAliasExecutesBuiltinWithOriginalIdentity(t *testing.T) {
	internalID := toolx.BuiltinConversationContext.Code
	runInput := RunInput{Conversation: models.Conversation{CustomerName: "Alias contract fixture"}}
	turn := agentLoopTurn{
		AllowedTools: []string{internalID},
		ToolPolicy:   parseAgentLoopToolPolicy(""),
	}
	state := agentLoopExecutionState{}
	var records []svc.AgentLoopToolCallInput
	execute := NewAgentLoopEngine().toolSearchExecutor(runInput, turn, &state, &records)
	definitions := append(agentLoopToolDefinitions(turn), agentLoopDecisionTool)

	tools, aliases, err := buildEinoToolSet(definitions, execute)
	if err != nil {
		t.Fatalf("build Eino tool set: %v", err)
	}
	providerName, ok := aliases.providerName(internalID)
	if !ok {
		t.Fatalf("provider alias missing for builtin %q", internalID)
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools})
	if err != nil {
		t.Fatalf("create Eino ToolNode: %v", err)
	}
	results, err := node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-builtin", Function: schema.FunctionCall{Name: providerName, Arguments: "{}"},
	}}))
	if err != nil {
		t.Fatalf("execute registered provider alias: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "Alias contract fixture") {
		t.Fatalf("builtin result did not return through the Eino tool loop: %#v", results)
	}
	if len(records) != 1 || records[0].ToolCode != internalID || records[0].Status != "completed" {
		t.Fatalf("builtin was not audited with its original internal identity: %#v", records)
	}
	if records[0].ToolCode == providerName {
		t.Fatalf("provider alias leaked into internal audit identity: %q", records[0].ToolCode)
	}
}
