// Package rbac provides role-based access control checks.
package rbac

import "github.com/NicolasHaas/gospeak/pkg/model"

// Permission represents a specific action that can be checked against a role.
type Permission int

const (
	PermCreateChannel Permission = iota
	PermDeleteChannel
	PermKickUser
	PermBanUser
	PermManageTokens
	PermEditChannel
	PermManageRoles
)

// permissionMatrix maps roles to their allowed permissions.
var permissionMatrix = map[model.Role]map[Permission]bool{
	model.RoleAdmin: {
		PermCreateChannel: true,
		PermDeleteChannel: true,
		PermKickUser:      true,
		PermBanUser:       true,
		PermManageTokens:  true,
		PermEditChannel:   true,
		PermManageRoles:   true,
	},
	model.RoleModerator: {
		PermKickUser: true,
	},
	model.RoleUser: {
		// No special permissions — can only join channels and talk
	},
}

// HasPermission checks if a role has a specific permission.
func HasPermission(role model.Role, perm Permission) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// RequirePermission returns an error message if the role lacks the permission, or empty string if allowed.
func RequirePermission(role model.Role, perm Permission) string {
	if HasPermission(role, perm) {
		return ""
	}
	return "permission denied: " + permName(perm) + " requires higher role"
}

func permName(p Permission) string {
	switch p {
	case PermCreateChannel:
		return "create_channel"
	case PermDeleteChannel:
		return "delete_channel"
	case PermKickUser:
		return "kick_user"
	case PermBanUser:
		return "ban_user"
	case PermManageTokens:
		return "manage_tokens"
	case PermEditChannel:
		return "edit_channel"
	case PermManageRoles:
		return "manage_roles"
	default:
		return "unknown"
	}
}
