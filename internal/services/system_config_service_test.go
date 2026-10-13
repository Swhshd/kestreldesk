package services

// This file has been modified for the KestrelDesk project.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestSupportNavigationMenuDefaultsAndPublicFiltering(t *testing.T) {
	setupSystemConfigServiceTestDB(t)

	defaultConfig := SystemConfigService.GetPublicSupportConfig()
	if len(defaultConfig.NavigationMenu) != 3 {
		t.Fatalf("expected default navigation menu, got %#v", defaultConfig.NavigationMenu)
	}

	raw, _ := json.Marshal([]request.SupportNavigationMenuItemRequest{
		{ID: "community", Title: "Community", URL: "/support/community/posts", Visible: boolPtr(true)},
		{ID: "hidden", Title: "Hidden", URL: "/support/hidden", Visible: boolPtr(false)},
		{ID: "docs", Title: "Docs", URL: "https://docs.example.com", OpenInNewWindow: true, Visible: boolPtr(true)},
	})
	config, err := SystemConfigService.SaveSupportConfig(map[string]json.RawMessage{
		systemConfigKeySupportNavMenu: raw,
	}, &dto.AuthPrincipal{UserID: 1, Username: "admin"})
	if err != nil {
		t.Fatalf("SaveSupportConfig() error = %v", err)
	}
	items := config.NavigationMenu
	if got := []int{items[0].SortNo, items[1].SortNo, items[2].SortNo}; got[0] != 10 || got[1] != 20 || got[2] != 30 {
		t.Fatalf("unexpected sort numbers: %#v", got)
	}

	publicConfig := SystemConfigService.GetPublicSupportConfig()
	if len(publicConfig.NavigationMenu) != 2 {
		t.Fatalf("expected only enabled public items, got %#v", publicConfig.NavigationMenu)
	}
	if publicConfig.NavigationMenu[0].ID != "community" || publicConfig.NavigationMenu[1].ID != "docs" {
		t.Fatalf("unexpected public order: %#v", publicConfig.NavigationMenu)
	}
	if !publicConfig.NavigationMenu[1].OpenInNewWindow {
		t.Fatalf("expected docs to open in new window")
	}
}

func TestSaveSupportConfigValidation(t *testing.T) {
	setupSystemConfigServiceTestDB(t)

	tests := []struct {
		name string
		req  []request.SupportNavigationMenuItemRequest
	}{
		{
			name: "empty",
			req:  nil,
		},
		{
			name: "blank title",
			req: []request.SupportNavigationMenuItemRequest{
				{Title: " ", URL: "/support", Visible: boolPtr(true)},
			},
		},
		{
			name: "invalid url",
			req: []request.SupportNavigationMenuItemRequest{
				{Title: "Docs", URL: "javascript:alert(1)", Visible: boolPtr(true)},
			},
		},
		{
			name: "no visible item",
			req: []request.SupportNavigationMenuItemRequest{
				{Title: "Docs", URL: "/support", Visible: boolPtr(false)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.req)
			err := SystemConfigService.SaveGroupConfig(systemConfigGroupSupportCenter, map[string]json.RawMessage{
				systemConfigKeySupportNavMenu: raw,
			}, nil)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if _, ok := err.(*SystemConfigValidationError); !ok {
				t.Fatalf("expected SystemConfigValidationError, got %T", err)
			}
		})
	}
}

func TestSaveSupportConfigRejectsUnsupportedKey(t *testing.T) {
	setupSystemConfigServiceTestDB(t)

	err := SystemConfigService.SaveGroupConfig(systemConfigGroupSupportCenter, map[string]json.RawMessage{
		"unknown": json.RawMessage(`true`),
	}, nil)
	if err == nil {
		t.Fatal("expected unsupported key error")
	}
}

func TestSupportDefaultWebChannelValidation(t *testing.T) {
	tests := []struct {
		name       string
		channel    *models.Channel
		value      string
		wantErr    bool
		wantStored string
	}{
		{
			name: "enabled web channel is accepted",
			channel: &models.Channel{
				ChannelID:   "web-channel-1",
				ChannelType: enums.ChannelTypeWeb,
				Status:      enums.StatusOk,
			},
			value:      "web-channel-1",
			wantStored: `"web-channel-1"`,
		},
		{
			name:       "blank value clears the default",
			value:      "",
			wantStored: `""`,
		},
		{
			name:    "missing channel is rejected",
			value:   "missing-channel",
			wantErr: true,
		},
		{
			name: "disabled channel is rejected",
			channel: &models.Channel{
				ChannelID:   "disabled-web-channel",
				ChannelType: enums.ChannelTypeWeb,
				Status:      enums.StatusDisabled,
			},
			value:   "disabled-web-channel",
			wantErr: true,
		},
		{
			name: "non web channel is rejected",
			channel: &models.Channel{
				ChannelID:   "enabled-non-web-channel",
				ChannelType: enums.ChannelTypeWxWorkKF,
				Status:      enums.StatusOk,
			},
			value:   "enabled-non-web-channel",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupSystemConfigServiceTestDB(t)
			if tt.channel != nil {
				if err := db.Create(tt.channel).Error; err != nil {
					t.Fatalf("create channel: %v", err)
				}
			}
			raw, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("marshal channel ID: %v", err)
			}
			err = SystemConfigService.SaveGroupConfig(systemConfigGroupSupportCenter, map[string]json.RawMessage{
				"defaultWebChannelId": raw,
			}, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected channel validation error")
				}
				if _, ok := err.(*SystemConfigValidationError); !ok {
					t.Fatalf("expected SystemConfigValidationError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SaveGroupConfig() error = %v", err)
			}
			var item models.SystemConfig
			if err := db.Where("group_code = ? AND config_key = ?", systemConfigGroupSupportCenter, "defaultWebChannelId").Take(&item).Error; err != nil {
				t.Fatalf("load stored config: %v", err)
			}
			if item.ConfigValue != tt.wantStored {
				t.Fatalf("stored config value = %q, want %q", item.ConfigValue, tt.wantStored)
			}
		})
	}
}

func TestSupportDefaultWebChannelResponsesOmitStaleDefaults(t *testing.T) {
	tests := []struct {
		name    string
		channel *models.Channel
	}{
		{name: "missing channel"},
		{
			name:    "disabled channel",
			channel: &models.Channel{ChannelID: "stale-channel", ChannelType: enums.ChannelTypeWeb, Status: enums.StatusDisabled},
		},
		{
			name:    "non web channel",
			channel: &models.Channel{ChannelID: "stale-channel", ChannelType: enums.ChannelTypeWxWorkKF, Status: enums.StatusOk},
		},
		{
			name:    "deleted channel",
			channel: &models.Channel{ChannelID: "stale-channel", ChannelType: enums.ChannelTypeWeb, Status: enums.StatusDeleted},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupSystemConfigServiceTestDB(t)
			if tt.channel != nil {
				if err := db.Create(tt.channel).Error; err != nil {
					t.Fatalf("create channel: %v", err)
				}
			}
			if err := db.Create(&models.SystemConfig{
				ConfigKey: "defaultWebChannelId", ConfigValue: `"stale-channel"`, GroupCode: systemConfigGroupSupportCenter,
			}).Error; err != nil {
				t.Fatalf("create stale default config: %v", err)
			}

			publicJSON, err := json.Marshal(SystemConfigService.GetPublicSupportConfig())
			if err != nil {
				t.Fatalf("marshal public support config: %v", err)
			}
			dashboardJSON, err := json.Marshal(SystemConfigService.GetDashboardSupportConfig())
			if err != nil {
				t.Fatalf("marshal dashboard support config: %v", err)
			}
			assertSupportDefaultChannelJSON(t, "public", publicJSON, "")
			assertSupportDefaultChannelJSON(t, "dashboard", dashboardJSON, "")
		})
	}
}

func TestPublicSupportConfigDefaultWebChannelExcludesSecrets(t *testing.T) {
	db := setupSystemConfigServiceTestDB(t)
	if err := db.Create(&models.Channel{
		ChannelID: "safe-public-channel", ChannelType: enums.ChannelTypeWeb, Status: enums.StatusOk,
		ConfigJSON: `{"apiKey":"channel-secret-sentinel","privateConfig":"must-not-leak"}`,
	}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.Create(&models.SystemConfig{
		ConfigKey: "defaultWebChannelId", ConfigValue: `"safe-public-channel"`, GroupCode: systemConfigGroupSupportCenter,
	}).Error; err != nil {
		t.Fatalf("create default config: %v", err)
	}

	encoded, err := json.Marshal(SystemConfigService.GetPublicSupportConfig())
	if err != nil {
		t.Fatalf("marshal public support config: %v", err)
	}
	assertSupportDefaultChannelJSON(t, "public", encoded, "safe-public-channel")
	for _, forbidden := range []string{"channel-secret-sentinel", "must-not-leak", "configJSON", "apiKey"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public support config leaked %q: %s", forbidden, encoded)
		}
	}
}

func assertSupportDefaultChannelJSON(t *testing.T, responseShape string, encoded []byte, want string) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode %s response: %v", responseShape, err)
	}
	var got string
	if raw, ok := payload["defaultWebChannelId"]; !ok {
		t.Fatalf("%s response missing defaultWebChannelId: %s", responseShape, encoded)
	} else if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s defaultWebChannelId: %v", responseShape, err)
	}
	if got != want {
		t.Fatalf("%s defaultWebChannelId = %q, want %q", responseShape, got, want)
	}
}

func setupSystemConfigServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   "t_",
			SingularTable: true,
		},
	})
	if err != nil {
		t.Fatalf("open sqlite error = %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&models.SystemConfig{}, &models.Channel{}); err != nil {
		t.Fatalf("auto migrate error = %v", err)
	}
	sqls.SetDB(db)
	return db
}

func boolPtr(value bool) *bool {
	return &value
}
