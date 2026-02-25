package model

// Role represents a user's permission level.
type Role int

const (
	RoleUser      Role = iota // Default role, can join channels and talk
	RoleModerator             // Can kick users
	RoleAdmin                 // Full control: create/delete channels, manage tokens, kick, ban
)

func (r Role) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleModerator:
		return "moderator"
	case RoleAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// ParseRole converts a string to a Role.
func ParseRole(s string) Role {
	switch s {
	case "admin":
		return RoleAdmin
	case "moderator":
		return RoleModerator
	default:
		return RoleUser
	}
}

// Valid returns true if the role is a recognised value (User, Moderator, or Admin).
func (r Role) Valid() bool {
	return r >= RoleUser && r <= RoleAdmin
}
