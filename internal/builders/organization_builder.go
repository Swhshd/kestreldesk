package builders

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
)

func BuildOrganization(item *models.Organization, role string, isActive bool) *response.OrganizationResponse {
	if item == nil {
		return nil
	}
	return &response.OrganizationResponse{
		ID:        item.ID,
		Code:      item.Code,
		Name:      item.Name,
		Logo:      item.Logo,
		Plan:      item.Plan,
		Status:    item.Status,
		Role:      role,
		IsActive:  isActive,
		CreatedAt: item.CreatedAt,
	}
}
