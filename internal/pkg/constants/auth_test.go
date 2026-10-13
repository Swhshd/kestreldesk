package constants

// This file has been modified for the KestrelDesk project.

import "testing"

func TestConversationClaimPermissionRegistry(t *testing.T) {
	p, ok := PermissionMap["conversation.claim"]
	if !ok || p.Method != "POST" || p.APIPath != "/api/dashboard/conversation/claim" {
		t.Fatal("dedicated claim permission missing")
	}
	for _, role := range []string{RoleCodeCsUser, RoleCodeCsTeamLeader, RoleCodeSuperAdmin} {
		found := false
		for _, permission := range RolePermissions[role] {
			if permission.Code == "conversation.claim" {
				found = true
			}
			if role == RoleCodeCsUser && permission.Code == "conversation.assign" {
				t.Fatal("ordinary agent received arbitrary assignment")
			}
		}
		if !found {
			t.Fatalf("claim permission missing from %s", role)
		}
	}
}

func TestCsUserGetsOnlyScopedConversationMutationPermissions(t *testing.T) {
	allowed := map[string]bool{
		"conversation.view":  true,
		"conversation.claim": true,
		"conversation.send":  true,
		"conversation.close": true,
		"conversation.tag":   true,
	}
	denied := []string{
		"conversation.assign",
		"conversation.transfer",
		"conversation.recycle",
		"conversation.recall",
		"conversation.delete",
	}
	got := map[string]bool{}
	for _, permission := range RolePermissions[RoleCodeCsUser] {
		got[permission.Code] = true
	}
	for code := range allowed {
		if !got[code] {
			t.Errorf("cs_user is missing required permission %q", code)
		}
	}
	for _, code := range denied {
		if got[code] {
			t.Errorf("cs_user received prohibited permission %q", code)
		}
	}

	recall, ok := PermissionMap["conversation.recall"]
	if !ok || recall.Method != "POST" || recall.APIPath != "/api/dashboard/conversation/recall_message" {
		t.Fatalf("separate recall permission missing or misconfigured: %+v", recall)
	}
	for _, role := range []string{RoleCodeSuperAdmin, RoleCodeAdmin, RoleCodeCsTeamLeader} {
		found := false
		for _, permission := range RolePermissions[role] {
			if permission.Code == "conversation.recall" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("recall permission missing from %s", role)
		}
	}
}

func TestTicketAssignmentPermissionIsLimitedToPrivilegedRoles(t *testing.T) {
	hasPermission := func(roleCode, permissionCode string) bool {
		for _, permission := range RolePermissions[roleCode] {
			if permission.Code == permissionCode {
				return true
			}
		}
		return false
	}

	if hasPermission(RoleCodeCsUser, PermissionTicketAssign.Code) {
		t.Fatal("ordinary support agents must not receive ticket.assign")
	}
	for _, roleCode := range []string{RoleCodeCsTeamLeader, RoleCodeAdmin, RoleCodeSuperAdmin} {
		if !hasPermission(roleCode, PermissionTicketAssign.Code) {
			t.Errorf("ticket.assign missing from privileged role %s", roleCode)
		}
	}
}

func TestBuiltinAuthSeedNamesDefaultToEnglish(t *testing.T) {
	t.Parallel()

	if BootstrapAdminNickname != "Super Admin" {
		t.Fatalf("BootstrapAdminNickname = %q, want %q", BootstrapAdminNickname, "Super Admin")
	}

	roles := map[string]string{}
	for _, role := range Roles {
		roles[role.Code] = role.Name
	}

	tests := map[string]string{
		RoleCodeSuperAdmin:   "Super Admin",
		RoleCodeAdmin:        "Admin",
		RoleCodeCsTeamLeader: "Support Team Lead",
		RoleCodeCsUser:       "Support Agent",
	}
	for code, want := range tests {
		if got := roles[code]; got != want {
			t.Fatalf("role %s name = %q, want %q", code, got, want)
		}
	}

	permissions := map[string]string{}
	for _, permission := range Permissions {
		permissions[permission.Code] = permission.Name
	}

	permissionTests := map[string]string{
		"user.view":         "View users",
		"ticket.create":     "Create tickets",
		"conversation.send": "Send conversation messages",
		"channel.view":      "View channels",
		"wxworkOutbox.view": "View WeCom outbox records",
		"agent.view":        "View agents",
	}
	for code, want := range permissionTests {
		if got := permissions[code]; got != want {
			t.Fatalf("permission %s name = %q, want %q", code, got, want)
		}
	}
}
