package channel

// This file has been modified for the KestrelDesk project.

import (
	"fmt"
	"github.com/Swhshd/kestreldesk/cmd/testdata/seedlang"
	"github.com/Swhshd/kestreldesk/cmd/testdata/seeds"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"time"

	"github.com/mlogclub/simple/common/strs"
	"github.com/mlogclub/simple/sqls"
)

type InitResult struct {
	Created int
	Updated int
}

// Init 初始化 Channel 测试数据。
// 依赖于 AI Agent 已初始化。
func Init(lang seedlang.Language) (*InitResult, error) {
	result := &InitResult{}

	aiAgentID, err := getDefaultAIAgentID()
	if err != nil {
		return result, fmt.Errorf("get default ai agent id failed: %w", err)
	}
	if aiAgentID == 0 {
		return result, fmt.Errorf("no default ai agent found, please init ai agent first")
	}

	seedItems := buildModels(lang, aiAgentID)
	for _, item := range seedItems {
		itemCopy := item
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			existing := repositories.ChannelRepository.Take(ctx.Tx, "name = ? AND channel_type = ?", itemCopy.Name, itemCopy.ChannelType)
			if existing != nil {
				if err := ctx.Tx.Model(existing).Updates(&itemCopy).Error; err != nil {
					return err
				}
				result.Updated++
			} else {
				if err := ctx.Tx.Create(&itemCopy).Error; err != nil {
					return err
				}
				result.Created++
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("upsert channel failed: %w", err)
		}
	}

	return result, nil
}

func buildModels(lang seedlang.Language, aiAgentID int64) []models.Channel {
	now := time.Now()
	seedItems := seeds.ChannelSeeds(lang)
	items := make([]models.Channel, 0, len(seedItems))
	for _, seed := range seedItems {
		items = append(items, models.Channel{
			Name:        seed.Name,
			ChannelType: seed.ChannelType,
			ChannelID:   strs.UUID(),
			AIAgentID:   aiAgentID,
			ConfigJSON:  seed.ConfigJSON,
			Status:      enums.StatusOk,
			Remark:      seed.Remark,
			AuditFields: models.AuditFields{
				CreatedAt:      now,
				CreateUserID:   0,
				CreateUserName: "System",
				UpdatedAt:      now,
				UpdateUserID:   0,
				UpdateUserName: "System",
			},
		})
	}
	return items
}

func getDefaultAIAgentID() (int64, error) {
	aiAgent := repositories.AIAgentRepository.Take(
		sqls.DB(),
		"status = ?",
		enums.StatusOk,
	)
	if aiAgent == nil {
		return 0, nil
	}
	return aiAgent.ID, nil
}
