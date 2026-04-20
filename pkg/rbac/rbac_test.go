package rbac

import (
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestHasPermission_AdminHasAllPermissions(t *testing.T) {
	allPerms := []Permission{
		PermCreateChannel,
		PermDeleteChannel,
		PermKickUser,
		PermBanUser,
		PermManageTokens,
		PermEditChannel,
		PermManageRoles,
	}

	for _, p := range allPerms {
		t.Run(permName(p), func(t *testing.T) {
			if got := HasPermission(model.RoleAdmin, p); !got {
				t.Errorf("HasPermission(RoleAdmin, %s) = false, want true", permName(p))
			}
		})
	}
}

func TestHasPermission_ModeratorOnlyKickUser(t *testing.T) {
	tests := []struct {
		name string
		perm Permission
		want bool
	}{
		{"create_channel", PermCreateChannel, false},
		{"delete_channel", PermDeleteChannel, false},
		{"kick_user", PermKickUser, true},
		{"ban_user", PermBanUser, false},
		{"manage_tokens", PermManageTokens, false},
		{"edit_channel", PermEditChannel, false},
		{"manage_roles", PermManageRoles, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPermission(model.RoleModerator, tt.perm); got != tt.want {
				t.Errorf("HasPermission(RoleModerator, %s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestHasPermission_UserHasNoPermissions(t *testing.T) {
	allPerms := []Permission{
		PermCreateChannel,
		PermDeleteChannel,
		PermKickUser,
		PermBanUser,
		PermManageTokens,
		PermEditChannel,
		PermManageRoles,
	}

	for _, p := range allPerms {
		t.Run(permName(p), func(t *testing.T) {
			if got := HasPermission(model.RoleUser, p); got {
				t.Errorf("HasPermission(RoleUser, %s) = true, want false", permName(p))
			}
		})
	}
}

func TestHasPermission_UnknownRoleDenied(t *testing.T) {
	tests := []struct {
		name string
		role model.Role
		perm Permission
	}{
		{"negative role", model.Role(-1), PermKickUser},
		{"large role", model.Role(99), PermCreateChannel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPermission(tt.role, tt.perm); got {
				t.Errorf("HasPermission(%d, %s) = true, want false", tt.role, permName(tt.perm))
			}
		})
	}
}

func TestRequirePermission_ReturnsEmptyStringWhenAllowed(t *testing.T) {
	tests := []struct {
		name string
		role model.Role
		perm Permission
	}{
		{"admin create_channel", model.RoleAdmin, PermCreateChannel},
		{"admin manage_roles", model.RoleAdmin, PermManageRoles},
		{"moderator kick_user", model.RoleModerator, PermKickUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequirePermission(tt.role, tt.perm); got != "" {
				t.Errorf("RequirePermission(%d, %s) = %q, want empty string", tt.role, permName(tt.perm), got)
			}
		})
	}
}

func TestRequirePermission_ReturnsDeniedMessageWhenForbidden(t *testing.T) {
	tests := []struct {
		name          string
		role          model.Role
		perm          Permission
		wantSubstring string
	}{
		{"user kick_user", model.RoleUser, PermKickUser, "kick_user"},
		{"moderator ban_user", model.RoleModerator, PermBanUser, "ban_user"},
		{"moderator manage_roles", model.RoleModerator, PermManageRoles, "manage_roles"},
		{"unknown role", model.Role(99), PermEditChannel, "edit_channel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RequirePermission(tt.role, tt.perm)
			if got == "" {
				t.Fatalf("RequirePermission(%d, %s) = empty string, want denial message", tt.role, permName(tt.perm))
			}
			if !strings.Contains(got, "permission denied:") {
				t.Fatalf("message %q does not contain %q", got, "permission denied:")
			}
			if !strings.Contains(got, tt.wantSubstring) {
				t.Fatalf("message %q does not contain perm %q", got, tt.wantSubstring)
			}
		})
	}
}

func TestRequirePermission_UnknownPermissionUsesUnknownName(t *testing.T) {
	got := RequirePermission(model.RoleUser, Permission(999))
	if got == "" {
		t.Fatalf("RequirePermission(RoleUser, Permission(999)) = empty string, want denial message")
	}
	if !strings.Contains(got, "unknown") {
		t.Fatalf("message %q does not contain %q", got, "unknown")
	}
}
