package builders

// This file has been modified for the KestrelDesk project.

import (
	"encoding/json"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
)

func BuildSkillDefinitionResponse(item *models.SkillDefinition) response.SkillDefinitionResponse {
	examples := make([]string, 0)
	if raw := item.Examples; raw != "" {
		_ = json.Unmarshal([]byte(raw), &examples)
	}
	toolWhitelist := make([]string, 0)
	if raw := item.ToolWhitelist; raw != "" {
		_ = json.Unmarshal([]byte(raw), &toolWhitelist)
	}
	return response.SkillDefinitionResponse{
		ID:             item.ID,
		Name:           item.Name,
		Description:    item.Description,
		Instruction:    item.Instruction,
		Examples:       examples,
		ToolWhitelist:  toolWhitelist,
		Status:         int(item.Status),
		StatusName:     getSkillStatusName(item.Status),
		Remark:         item.Remark,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		CreateUserName: item.CreateUserName,
		UpdateUserName: item.UpdateUserName,
	}
}

func getSkillStatusName(status enums.Status) string {
	if label := enums.GetStatusLabel(status); label != "" {
		return label
	}
	return "未知"
}
