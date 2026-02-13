// Package rbac provides role-based access control checks.
package rbac

import "github.com/NicolasHaas/gospeak/pkg/model"

// permissionMatrix maps roles to their allowed permissions.
var permissionMatrix = map[model.Role]map[model.Permission]bool{
	model.RoleAdmin: {
		model.PermCreateChannel: true,
		model.PermDeleteChannel: true,
		model.PermKickUser:      true,
		model.PermBanUser:       true,
		model.PermManageTokens:  true,
		model.PermEditChannel:   true,
		model.PermManageRoles:   true,
	},
	model.RoleModerator: {
		model.PermKickUser: true,
	},
	model.RoleUser: {
		// No special permissions — can only join channels and talk
	},
}

// HasPermission checks if a role has a specific permission.
func HasPermission(role model.Role, perm model.Permission) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// RequirePermission returns an error message if the role lacks the permission, or empty string if allowed.
func RequirePermission(role model.Role, perm model.Permission) string {
	if HasPermission(role, perm) {
		return ""
	}
	return "permission denied: " + permName(perm) + " requires higher role"
}

func permName(p model.Permission) string {
	switch p {
	case model.PermCreateChannel:
		return "create_channel"
	case model.PermDeleteChannel:
		return "delete_channel"
	case model.PermKickUser:
		return "kick_user"
	case model.PermBanUser:
		return "ban_user"
	case model.PermManageTokens:
		return "manage_tokens"
	case model.PermEditChannel:
		return "edit_channel"
	case model.PermManageRoles:
		return "manage_roles"
	default:
		return "unknown"
	}
}
