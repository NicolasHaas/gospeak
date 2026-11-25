// Package pb contains the hand-maintained control message structs mirrored in proto/control.proto.

package pb

// ControlMessage wraps all control plane messages.
type ControlMessage struct {
	PersonalTokenStatusReq     *PersonalTokenStatusRequest     `json:"personal_token_status_request,omitempty"`
	PersonalTokenStatusResp    *PersonalTokenStatusResponse    `json:"personal_token_status_response,omitempty"`
	RotatePersonalTokenReq     *RotatePersonalTokenRequest     `json:"rotate_personal_token_request,omitempty"`
	RotatePersonalTokenResp    *RotatePersonalTokenResponse    `json:"rotate_personal_token_response,omitempty"`
	RevokePersonalTokenReq     *RevokePersonalTokenRequest     `json:"revoke_personal_token_request,omitempty"`
	RevokePersonalTokenResp    *RevokePersonalTokenResponse    `json:"revoke_personal_token_response,omitempty"`
	IssuePersonalRecoveryReq   *IssuePersonalRecoveryRequest   `json:"issue_personal_recovery_request,omitempty"`
	IssuePersonalRecoveryResp  *IssuePersonalRecoveryResponse  `json:"issue_personal_recovery_response,omitempty"`
	RevokePersonalRecoveryReq  *RevokePersonalRecoveryRequest  `json:"revoke_personal_recovery_request,omitempty"`
	RevokePersonalRecoveryResp *RevokePersonalRecoveryResponse `json:"revoke_personal_recovery_response,omitempty"`
	RedeemPersonalRecoveryReq  *RedeemPersonalRecoveryRequest  `json:"redeem_personal_recovery_request,omitempty"`
	RedeemPersonalRecoveryResp *RedeemPersonalRecoveryResponse `json:"redeem_personal_recovery_response,omitempty"`
	ListInvitesReq             *ListInvitesRequest             `json:"list_invites_request,omitempty"`
	ListInvitesResp            *ListInvitesResponse            `json:"list_invites_response,omitempty"`
	RevokeInviteReq            *RevokeInviteRequest            `json:"revoke_invite_request,omitempty"`
	RevokeInviteResp           *RevokeInviteResponse           `json:"revoke_invite_response,omitempty"`
	// Only one of these fields should be set.
	AuthRequest         *AuthRequest                   `json:"auth_request,omitempty"`
	AuthResponse        *AuthResponse                  `json:"auth_response,omitempty"`
	ChannelListRequest  *ChannelListRequest            `json:"channel_list_request,omitempty"`
	ChannelListResponse *ChannelListResponse           `json:"channel_list_response,omitempty"`
	JoinChannelRequest  *JoinChannelRequest            `json:"join_channel_request,omitempty"`
	ChannelJoinResponse *ChannelJoinResponse           `json:"channel_join_response,omitempty"`
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
	ListBansReq         *ListBansRequest               `json:"list_bans_request,omitempty"`
	ListBansResp        *ListBansResponse              `json:"list_bans_response,omitempty"`
	UnbanReq            *UnbanRequest                  `json:"unban_request,omitempty"`
	UnbanResp           *UnbanResponse                 `json:"unban_response,omitempty"`
	ChatMsg             *ChatMessage                   `json:"chat_message,omitempty"`
	ChatEvent           *ChatMessage                   `json:"chat_event,omitempty"`
	ChatHistoryReq      *ChatHistoryRequest            `json:"chat_history_request,omitempty"`
	ChatHistoryResp     *ChatHistoryResponse           `json:"chat_history_response,omitempty"`
	ChatDeleteReq       *ChatDeleteRequest             `json:"chat_delete_request,omitempty"`
	ChatDeleteEvent     *ChatDeleteEvent               `json:"chat_delete_event,omitempty"`
	ScreenShareStartReq *ScreenShareStartRequest       `json:"screen_share_start_request,omitempty"`
	ScreenShareStopReq  *ScreenShareStopRequest        `json:"screen_share_stop_request,omitempty"`
	ScreenShareSubReq   *ScreenShareSubscribeRequest   `json:"screen_share_subscribe_request,omitempty"`
	ScreenShareShareReq *ScreenShareShareRequest       `json:"screen_share_share_request,omitempty"`
	ScreenShareUnsubReq *ScreenShareUnsubscribeRequest `json:"screen_share_unsubscribe_request,omitempty"`
	ScreenShareEvent    *ScreenShareEvent              `json:"screen_share_event,omitempty"`
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

// ----- Personal credentials -----

type PersonalTokenStatusRequest struct {
	TargetID int64 `json:"target_id"` // zero selects the authenticated account
}

type PersonalTokenStatusResponse struct {
	TargetID     int64  `json:"target_id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	ChannelScope int64  `json:"channel_scope"`
	Version      int64  `json:"version"`
	Error        string `json:"error,omitempty"`
}

type RotatePersonalTokenRequest struct {
	Version      int64  `json:"version"`
	CurrentToken string `json:"current_token"`
	Replacement  string `json:"replacement"`
}

type RotatePersonalTokenResponse struct {
	Error string `json:"error,omitempty"`
}

type RevokePersonalTokenRequest struct {
	TargetID int64 `json:"target_id"`
	Version  int64 `json:"version"`
}

type RevokePersonalTokenResponse struct {
	Error string `json:"error,omitempty"`
}

type IssuePersonalRecoveryRequest struct {
	TargetID int64 `json:"target_id"`
	Version  int64 `json:"version"`
}

type IssuePersonalRecoveryResponse struct {
	TargetID  int64  `json:"target_id"`
	Grant     string `json:"grant,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	Version   int64  `json:"version"`
	Error     string `json:"error,omitempty"`
}

type RevokePersonalRecoveryRequest struct {
	TargetID int64 `json:"target_id"`
	Version  int64 `json:"version"`
}

type RevokePersonalRecoveryResponse struct {
	Error string `json:"error,omitempty"`
}

type RedeemPersonalRecoveryRequest struct {
	TargetID    int64  `json:"target_id"`
	Username    string `json:"username"`
	Grant       string `json:"grant"`
	Replacement string `json:"replacement"`
}

type RedeemPersonalRecoveryResponse struct {
	Error string `json:"error,omitempty"`
}

// ----- Auth -----

type AuthRequest struct {
	PersonalOnly bool     `json:"personal_only,omitempty"`
	Token        string   `json:"token"` // empty = token-less join (if server allows)
	Username     string   `json:"username"`
	MediaCiphers []string `json:"media_ciphers"`
}

type AuthResponse struct {
	SessionID            uint32        `json:"session_id"`
	Username             string        `json:"username"`
	Role                 string        `json:"role"`
	ChannelScope         int64         `json:"channel_scope,omitempty"`
	EncryptionKey        []byte        `json:"encryption_key"`
	MediaCipher          string        `json:"media_cipher"`
	VoiceRegistrationKey []byte        `json:"voice_registration_key"`
	Channels             []ChannelInfo `json:"channels"`
	ScreenAddr           string        `json:"screen_addr,omitempty"`
	ScreenAuthToken      string        `json:"screen_auth_token,omitempty"`
	ScreenShareEnabled   bool          `json:"screen_share_enabled,omitempty"`
	AutoToken            string        `json:"auto_token,omitempty"` // set when server generated a token for this user
	HistoryRequestIDs    bool          `json:"history_request_ids,omitempty"`
	LiveSessionIDs       []uint32      `json:"live_session_ids"`
	UserID               int64         `json:"user_id,omitempty"`
	CredentialVersion    int64         `json:"credential_version,omitempty"`
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
	SessionID uint32 `json:"session_id,omitempty"`
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Muted     bool   `json:"muted"`
	Deafened  bool   `json:"deafened"`
}

type ChannelListRequest struct{}

type ChannelListResponse struct {
	Channels []ChannelInfo `json:"channels"`
}

type JoinChannelRequest struct {
	ChannelID int64 `json:"channel_id"`
}

type ChannelJoinResponse struct {
	ChannelID int64  `json:"channel_id"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
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
	LiveSessionIDs     []uint32      `json:"live_session_ids"`
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

type ListInvitesRequest struct {
	AfterID int64 `json:"after_id,omitempty"`
	Limit   int   `json:"limit,omitempty"`
}

type InviteInfo struct {
	ID           int64  `json:"id"`
	Role         string `json:"role"`
	ChannelScope int64  `json:"channel_scope"`
	CreatedBy    int64  `json:"created_by"`
	MaxUses      int64  `json:"max_uses"`
	UseCount     int64  `json:"use_count"`
	ExpiresAt    int64  `json:"expires_at"`
	CreatedAt    int64  `json:"created_at"`
}

type ListInvitesResponse struct {
	Invites []InviteInfo `json:"invites"`
	HasMore bool         `json:"has_more,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type RevokeInviteRequest struct {
	InviteID int64 `json:"invite_id"`
}
type RevokeInviteResponse struct {
	Revoked bool   `json:"revoked"`
	Error   string `json:"error,omitempty"`
}

type KickUserRequest struct {
	UserID int64  `json:"user_id"`
	Reason string `json:"reason"`
}

type BanUserRequest struct {
	UserID          int64  `json:"user_id"`
	Reason          string `json:"reason"`
	DurationSeconds int64  `json:"duration_seconds"`
	IPBanSessionID  uint32 `json:"ip_ban_session_id,omitempty"`
}

type ListBansRequest struct {
	AfterID int64 `json:"after_id,omitempty"`
	Limit   int   `json:"limit,omitempty"`
}

type BanInfo struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id,omitempty"`
	Username  string `json:"username,omitempty"`
	IP        string `json:"ip,omitempty"`
	BannedBy  int64  `json:"banned_by"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type ListBansResponse struct {
	Bans    []BanInfo `json:"bans"`
	HasMore bool      `json:"has_more,omitempty"`
}

type UnbanRequest struct {
	BanID int64 `json:"ban_id"`
}

type UnbanResponse struct {
	Success bool `json:"success"`
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
	ID         int64  `json:"id,omitempty"`
	ChannelID  int64  `json:"channel_id"`
	SenderID   int64  `json:"sender_id"`
	SenderName string `json:"sender_name"`
	Text       string `json:"text"`
	Timestamp  int64  `json:"timestamp"`
}

type ChatHistoryRequest struct {
	RequestID int64 `json:"request_id,omitempty"`
	ChannelID int64 `json:"channel_id"`
	BeforeID  int64 `json:"before_id,omitempty"`
	Limit     int64 `json:"limit,omitempty"`
}

type ChatHistoryResponse struct {
	RequestID int64         `json:"request_id,omitempty"`
	ChannelID int64         `json:"channel_id"`
	Messages  []ChatMessage `json:"messages"`
	HasMore   bool          `json:"has_more"`
}

type ChatDeleteRequest struct {
	ChannelID int64 `json:"channel_id"`
	MessageID int64 `json:"message_id"`
}

type ChatDeleteEvent struct {
	ChannelID int64 `json:"channel_id"`
	MessageID int64 `json:"message_id"`
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
	MediaCipher   string `json:"media_cipher,omitempty"`
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
