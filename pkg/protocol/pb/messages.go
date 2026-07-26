// Package pb contains the hand-maintained control message structs mirrored in proto/control.proto.

package pb

// ControlMessage wraps all control plane messages.
type ControlMessage struct {
	// Only one of these fields should be set.
	AuthRequest         *AuthRequest                   `json:"auth_request,omitempty"`
	AuthResponse        *AuthResponse                  `json:"auth_response,omitempty"`
	ChannelListRequest  *ChannelListRequest            `json:"channel_list_request,omitempty"`
	ChannelListResponse *ChannelListResponse           `json:"channel_list_response,omitempty"`
	JoinChannelRequest  *JoinChannelRequest            `json:"join_channel_request,omitempty"`
	LeaveChannelRequest *LeaveChannelRequest           `json:"leave_channel_request,omitempty"`
	ChannelJoinedEvent  *ChannelJoinedEvent            `json:"channel_joined_event,omitempty"`
	ChannelLeftEvent    *ChannelLeftEvent              `json:"channel_left_event,omitempty"`
	UserStateUpdate     *UserStateUpdate               `json:"user_state_update,omitempty"`
	ServerStateEvent    *ServerStateEvent              `json:"server_state_event,omitempty"`
	CreateChannelReq    *CreateChannelRequest          `json:"create_channel_request,omitempty"`
	DeleteChannelReq    *DeleteChannelRequest          `json:"delete_channel_request,omitempty"`
	CreateTokenReq      *CreateTokenRequest            `json:"create_token_request,omitempty"`
	CreateTokenResp     *CreateTokenResponse           `json:"create_token_response,omitempty"`
	KickUserReq         *KickUserRequest               `json:"kick_user_request,omitempty"`
	BanUserReq          *BanUserRequest                `json:"ban_user_request,omitempty"`
	ChatMsg             *ChatMessage                   `json:"chat_message,omitempty"`
	ChatEvent           *ChatMessage                   `json:"chat_event,omitempty"`
	ScreenShareStartReq *ScreenShareStartRequest       `json:"screen_share_start_request,omitempty"`
	ScreenShareStopReq  *ScreenShareStopRequest        `json:"screen_share_stop_request,omitempty"`
	ScreenShareSubReq   *ScreenShareSubscribeRequest   `json:"screen_share_subscribe_request,omitempty"`
	ScreenShareShareReq *ScreenShareShareRequest       `json:"screen_share_share_request,omitempty"`
	ScreenShareUnsubReq *ScreenShareUnsubscribeRequest `json:"screen_share_unsubscribe_request,omitempty"`
	ScreenShareEvent    *ScreenShareEvent              `json:"screen_share_event,omitempty"`
	ScreenShareFrame    *ScreenShareFrame              `json:"screen_share_frame,omitempty"`
	SetUserRoleReq      *SetUserRoleRequest            `json:"set_user_role_request,omitempty"`
	SetUserRoleResp     *SetUserRoleResponse           `json:"set_user_role_response,omitempty"`
	ExportDataReq       *ExportDataRequest             `json:"export_data_request,omitempty"`
	ExportDataResp      *ExportDataResponse            `json:"export_data_response,omitempty"`
	ImportChannelsReq   *ImportChannelsRequest         `json:"import_channels_request,omitempty"`
	ImportChannelsResp  *ImportChannelsResponse        `json:"import_channels_response,omitempty"`
	ErrorResponse       *ErrorResponse                 `json:"error_response,omitempty"`
	Ping                *Ping                          `json:"ping,omitempty"`
	Pong                *Pong                          `json:"pong,omitempty"`
}

// ----- Auth -----

type AuthRequest struct {
	Token    string `json:"token"` // empty = token-less join (if server allows)
	Username string `json:"username"`
}

type AuthResponse struct {
	SessionID            uint32        `json:"session_id"`
	Username             string        `json:"username"`
	Role                 string        `json:"role"`
	ChannelScope         int64         `json:"channel_scope,omitempty"`
	EncryptionKey        []byte        `json:"encryption_key"`
	VoiceRegistrationKey []byte        `json:"voice_registration_key"`
	Channels             []ChannelInfo `json:"channels"`
	ScreenAddr           string        `json:"screen_addr,omitempty"`
	ScreenAuthToken      string        `json:"screen_auth_token,omitempty"`
	ScreenShareEnabled   bool          `json:"screen_share_enabled,omitempty"`
	AutoToken            string        `json:"auto_token,omitempty"` // set when server generated a token for this user
}

// ----- Channels -----

type ChannelInfo struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	MaxUsers         int32      `json:"max_users"`
	ParentID         int64      `json:"parent_id"`
	IsTemp           bool       `json:"is_temp"`
	AllowSubChannels bool       `json:"allow_sub_channels"`
	Users            []UserInfo `json:"users"`
}

type UserInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Muted    bool   `json:"muted"`
	Deafened bool   `json:"deafened"`
}

type ChannelListRequest struct{}

type ChannelListResponse struct {
	Channels []ChannelInfo `json:"channels"`
}

type JoinChannelRequest struct {
	ChannelID int64 `json:"channel_id"`
}

type LeaveChannelRequest struct{}

// ----- Events -----

type ChannelJoinedEvent struct {
	ChannelID int64    `json:"channel_id"`
	User      UserInfo `json:"user"`
}

type ChannelLeftEvent struct {
	ChannelID int64  `json:"channel_id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
}

type UserStateUpdate struct {
	Muted    bool `json:"muted"`
	Deafened bool `json:"deafened"`
}

type ServerStateEvent struct {
	Channels           []ChannelInfo `json:"channels"`
	ScreenShareEnabled bool          `json:"screen_share_enabled,omitempty"`
}

// ----- Admin -----

type CreateChannelRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	MaxUsers         int32  `json:"max_users"`
	ParentID         int64  `json:"parent_id"`          // 0 = root channel
	IsTemp           bool   `json:"is_temp"`            // create as temporary
	AllowSubChannels bool   `json:"allow_sub_channels"` // allow sub-channel creation
}

type DeleteChannelRequest struct {
	ChannelID int64 `json:"channel_id"`
}

type CreateTokenRequest struct {
	Role             string `json:"role"`
	ChannelScope     int64  `json:"channel_scope"`
	MaxUses          int32  `json:"max_uses"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
}

type CreateTokenResponse struct {
	Token string `json:"token"`
}

type KickUserRequest struct {
	UserID int64  `json:"user_id"`
	Reason string `json:"reason"`
}

type BanUserRequest struct {
	UserID          int64  `json:"user_id"`
	Reason          string `json:"reason"`
	DurationSeconds int64  `json:"duration_seconds"`
}

// ----- Generic -----

type ErrorResponse struct {
	Code    int32  `json:"code"`
	Message string `json:"message"`
}

type Ping struct {
	Timestamp int64 `json:"timestamp"`
}

type Pong struct {
	Timestamp int64 `json:"timestamp"`
}

// ----- Chat -----

type ChatMessage struct {
	ChannelID  int64  `json:"channel_id"`
	SenderID   int64  `json:"sender_id"`
	SenderName string `json:"sender_name"`
	Text       string `json:"text"`
	Timestamp  int64  `json:"timestamp"`
}

// ----- Screen Sharing -----

type ScreenShareStartRequest struct {
	DisplayIndex int32 `json:"display_index"`
	Width        int32 `json:"width"`
	Height       int32 `json:"height"`
}

type ScreenShareStopRequest struct{}

type ScreenShareSubscribeRequest struct {
	ChannelID int64 `json:"channel_id"`
}

type ScreenShareShareRequest struct{}

type ScreenShareUnsubscribeRequest struct{}

type ScreenShareEvent struct {
	ChannelID     int64  `json:"channel_id"`
	SessionID     uint32 `json:"session_id"`
	UserID        int64  `json:"user_id"`
	Username      string `json:"username"`
	Active        bool   `json:"active"`
	Width         int32  `json:"width,omitempty"`
	Height        int32  `json:"height,omitempty"`
	Viewers       int32  `json:"viewers,omitempty"`
	EncryptionKey []byte `json:"encryption_key,omitempty"`
}

type ScreenShareFrame struct {
	ChannelID int64  `json:"channel_id"`
	SessionID uint32 `json:"session_id"`
	Width     int32  `json:"width"`
	Height    int32  `json:"height"`
	Format    string `json:"format"`
	Data      []byte `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

// ----- Role Management -----

type SetUserRoleRequest struct {
	TargetUserID int64  `json:"target_user_id"`
	NewRole      string `json:"new_role"`
}

type SetUserRoleResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ----- Export / Import -----

type ExportDataRequest struct {
	Type string `json:"type"` // "channels" or "users"
}

type ExportDataResponse struct {
	Type string `json:"type"`
	Data string `json:"data"` // YAML content
}

type ImportChannelsRequest struct {
	YAML string `json:"yaml"`
}

type ImportChannelsResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
