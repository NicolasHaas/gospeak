package datastore_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func NewTestSqlConn(t *testing.T) (*datastore.ProviderFactory, error) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	st, err := datastore.NewProviderFactory(dbPath)
	if err != nil {
		return nil, fmt.Errorf("store_test: failed to open db: %w", err)
	}

	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			fmt.Printf("Error closing database: %v\n", err)
		}
	})

	return st, nil
}

func generateRandomSafeString(t *testing.T, byteLength int) string {
	t.Helper()
	bytes := make([]byte, byteLength)
	// Use crypto/rand.Read to fill the byte slice with random bytes from the OS's secure random number generator.
	// This function does not need seeding and is safe for concurrent use.
	_, err := rand.Read(bytes)
	if err != nil {
		return ""
	}

	encoded := base64.URLEncoding.EncodeToString(bytes)
	return encoded
}

func TestZeroTime(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("failed to open test connection: %v", err)
	}

	if diff := cmp.Diff(time.Time{}, store.NonTx().ZeroTime()); diff != "" {
		t.Errorf("store.NonTx().ZeroTime mismatch (-want +got):\\n%s", diff)
	}
}

func TestCreateUser(t *testing.T) {
	t.Parallel()

	type tcase struct {
		username  string
		role      model.Role
		expectErr bool
	}

	tcases := map[string]tcase{
		"minimum_required_fields": {
			username:  "johndoe",
			role:      model.RoleUser,
			expectErr: false,
		},
		"injection_username": { // SQL injection contains invalid chars (quotes, spaces, equals)
			username:  "' OR '1'='1",
			role:      model.RoleAdmin,
			expectErr: true,
		},
		"empty_username": { // Empty username should not be allowed
			username:  "",
			role:      model.RoleUser,
			expectErr: true,
		},
		"full_username": { // 65 Character username is too long
			username:  "24433252080542468109190329288548376491503980265648043643151614656",
			role:      model.RoleUser,
			expectErr: true,
		},
		"over_privileged": { // Privilege does not exist
			username:  "janedoe",
			role:      10,
			expectErr: true,
		},
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			got, err := store.NonTx().CreateUser(tc.username, tc.role)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("CreateUser: expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateUser: unexpected error: %v", err)
			}

			want := &model.User{
				Username: tc.username,
				Role:     tc.role,
			}

			if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(model.User{}, "ID", "CreatedAt")); diff != "" {
				t.Errorf("store.NonTx().CreateUser mismatch (-want +got):\\n%s", diff)
			}
		}
	}

	for name, tc := range tcases {
		t.Run(name, fn(tc))
	}
}

func TestGetUserByUsername(t *testing.T) {
	t.Parallel()

	type tcase struct {
		username   string
		role       model.Role
		seedUser   bool
		expectUser bool
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			username:   "johndoe",
			role:       model.RoleUser,
			seedUser:   true,
			expectUser: true,
		},
		"no_user_exists": {
			username:   "janedoe",
			role:       model.RoleUser,
			seedUser:   false,
			expectUser: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			var seeded *model.User
			if tc.seedUser {
				u, err := store.NonTx().CreateUser(tc.username, tc.role)
				if err != nil {
					t.Fatalf("CreateUser: failed to seed user: %v", err)
				}
				seeded = u
			}

			got, err := store.NonTx().GetUserByUsername(tc.username)
			if !tc.expectUser {
				if got != nil {
					t.Fatalf("GetUserByUsername: expected nil, got user")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetUserByUsername: unexpected error: %v", err)
			}

			want := &model.User{
				Username: tc.username,
				Role:     tc.role,
			}

			if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(model.User{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("GetUserByUsername mismatch (-want +got):\n%s", diff)
			}

			if seeded != nil && got.ID != seeded.ID {
				t.Fatalf("expected same user ID as seeded; want %v got %v", seeded.ID, got.ID)
			}
		})
	}
}

func TestGetUserByID(t *testing.T) {
	t.Parallel()

	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("failed to open test connection: %v", err)
	}

	want := int64(1)

	_, err = store.NonTx().CreateUser("johndoe", model.RoleUser)
	if err != nil {
		t.Fatalf("CreateUser: failed to seed user: %v", err)
	}

	res, err := store.NonTx().GetUserByID(want)
	if err != nil {
		t.Fatalf("GetUserByID: unexpected error: %v", err)
	}

	got := res.ID

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("GetUserByID mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdateUserRole(t *testing.T) {
	t.Parallel()

	type tcase struct {
		username string
		role     model.Role
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			username: "johndoe",
			role:     model.RoleUser,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			u, err := store.NonTx().CreateUser(tc.username, tc.role)
			if err != nil {
				t.Fatalf("CreateUser: failed to seed user: %v", err)
			}

			if err := store.NonTx().UpdateUserRole(u.ID, model.RoleAdmin); err != nil {
				t.Fatalf("UpdateUserRole: unexpected error: %v", err)
			}

			want := &model.User{
				Username: tc.username,
				Role:     model.RoleAdmin,
			}

			got, err := store.NonTx().GetUserByID(u.ID)
			if err != nil {
				t.Fatalf("GetUserByID: unexpected error: %v", err)
			}

			if diff := cmp.Diff(want.Role, got.Role); diff != "" {
				t.Fatalf("UpdateUserRole mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestListUsers(t *testing.T) {
	t.Parallel()

	type tcase struct {
		users []model.User
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			users: []model.User{
				{
					Username: "johndoe",
					Role:     model.RoleUser,
				},
				{
					Username: "janedoe",
					Role:     model.RoleModerator,
				},
				{
					Username: "babydoe",
					Role:     model.RoleAdmin,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			for _, user := range tc.users {
				_, err := store.NonTx().CreateUser(user.Username, user.Role)
				if err != nil {
					t.Fatalf("CreateUser: failed to seed user: %v", err)
				}
			}

			users, err := store.NonTx().ListUsers()
			if err != nil {
				t.Fatalf("ListUsers: unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.users, users, cmpopts.IgnoreFields(model.User{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("ListUsers mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateChannelFull(t *testing.T) {
	t.Parallel()

	type tcase struct {
		inputChannel     *model.Channel
		expectedResponse *model.Channel
		expecErr         bool
	}

	channelName, channelDescription := "New Channel", "A brand new channel"
	channelMaxUsers := 10
	channelParentID := int64(10)
	channelIsTemp, channelAllowsSubs := true, true

	tests := map[string]tcase{
		"minimum_required_fields": {
			inputChannel: &model.Channel{
				Name:             channelName,
				Description:      channelDescription,
				MaxUsers:         channelMaxUsers,
				ParentID:         10,
				IsTemp:           channelIsTemp,
				AllowSubChannels: channelAllowsSubs,
			},
			expectedResponse: &model.Channel{
				Name:             channelName,
				Description:      channelDescription,
				MaxUsers:         channelMaxUsers,
				ParentID:         channelParentID,
				IsTemp:           channelIsTemp,
				AllowSubChannels: channelAllowsSubs,
			},
			expecErr: false,
		},
		"invalid_name": {
			inputChannel: &model.Channel{
				Name:             generateRandomSafeString(t, 65),
				Description:      channelDescription,
				MaxUsers:         channelMaxUsers,
				ParentID:         10,
				IsTemp:           channelIsTemp,
				AllowSubChannels: channelAllowsSubs,
			},
			expecErr: true,
		},
		"invalid_desc": {
			inputChannel: &model.Channel{
				Name:             channelName,
				Description:      generateRandomSafeString(t, 257),
				MaxUsers:         channelMaxUsers,
				ParentID:         10,
				IsTemp:           channelIsTemp,
				AllowSubChannels: channelAllowsSubs,
			},
			expecErr: true,
		},
		"invalid_max_users": {
			inputChannel: &model.Channel{
				Name:             channelName,
				Description:      channelDescription,
				MaxUsers:         257,
				ParentID:         10,
				IsTemp:           channelIsTemp,
				AllowSubChannels: channelAllowsSubs,
			},
			expecErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			err = store.NonTx().CreateChannel(tc.inputChannel)
			if tc.expecErr {
				if err == nil {
					t.Fatalf("CreateChannelFull: expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateChannelFull: unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.expectedResponse, tc.inputChannel, cmpopts.IgnoreFields(model.Channel{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("CreateChannelFull mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDeleteChannel(t *testing.T) {
	t.Parallel()

	type tcase struct {
		inputChannel *model.Channel
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			inputChannel: model.NewChannel(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			err = store.NonTx().CreateChannel(tc.inputChannel)
			if err != nil {
				t.Fatalf("CreateChannel: unexpected error: %v", err)
			}

			if err := store.NonTx().DeleteChannel(tc.inputChannel.ID); err != nil {
				t.Fatalf("DeleteChannel: unexpected error: %v", err)
			}

			deletedChannel, err := store.NonTx().GetChannel(tc.inputChannel.ID)
			if err != nil {
				t.Fatalf("GetChannel: unexpected error: %v", err)
			}
			if deletedChannel != nil {
				t.Fatalf("expected channel to be nil got: %+v", deletedChannel)
			}
		})
	}
}

func TestListChannels(t *testing.T) {
	t.Parallel()

	type tcase struct {
		inputChannel *model.Channel
		iter         int
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			inputChannel: model.NewChannel(),
			iter:         10,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			var expectedChannels = make(map[string]*model.Channel)
			for i := range tc.iter {
				channelName := fmt.Sprintf("%s_%d", tc.inputChannel.Name, i)
				channelDesc := fmt.Sprintf("%s_%d", tc.inputChannel.Description, i)

				tempChannel := &model.Channel{
					Name:        channelName,
					Description: channelDesc,
					MaxUsers:    i + 1,
					ParentID:    1,
				}

				err := store.NonTx().CreateChannel(tempChannel)
				if err != nil {
					t.Fatalf("CreateChannel: unexpected error: %v", err)
				}

				expectedChannels[channelName] = tempChannel
			}

			channelList, err := store.NonTx().ListChannels()
			if err != nil {
				t.Fatalf("ListChannels: unexpected error: %v", err)
			}

			if len(channelList) != tc.iter {
				t.Fatalf("ListChannels: length mistmatch got=%d want=%d", len(channelList), tc.iter)
			}

			for _, got := range channelList {
				want, ok := expectedChannels[got.Name]
				if !ok {
					t.Fatalf("ListChannels: unexpected channel returned: %+v", got)
				}

				if got.Description != want.Description {
					t.Errorf("ListChannels: channel description mismatch: got=%s want=%s", got.Description, want.Description)
				}
			}

		})
	}
}

func TestGetChannel(t *testing.T) {
	t.Parallel()

	type tcase struct {
		inputChannel *model.Channel
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			inputChannel: model.NewChannel(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			err = store.NonTx().CreateChannel(tc.inputChannel)
			if err != nil {
				t.Fatalf("CreateChannel: unexpected error: %v", err)
			}

			got, err := store.NonTx().GetChannel(tc.inputChannel.ID)
			if err != nil {
				t.Fatalf("GetChannel: unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.inputChannel, got, cmpopts.IgnoreFields(model.Channel{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("GetChannel mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetChannelByNameAndParent(t *testing.T) {
	t.Parallel()

	type tcase struct {
		parentChannel *model.Channel
		childChannel  *model.Channel
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			parentChannel: &model.Channel{
				Name:        "Parent",
				Description: "Parent Channel",
			},
			childChannel: &model.Channel{
				Name:        "Child",
				Description: "Child Channel",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			err = store.NonTx().CreateChannel(tc.parentChannel)
			if err != nil {
				t.Fatalf("CreateChannel: unexpected error creating parent: %v", err)
			}

			tc.childChannel.ParentID = tc.parentChannel.ID

			err = store.NonTx().CreateChannel(tc.childChannel)
			if err != nil {
				t.Fatalf("CreateChannel: unexpected error creating child: %v", err)
			}

			got, err := store.NonTx().GetChannelByNameAndParent(tc.childChannel.Name, tc.childChannel.ParentID)
			if err != nil {
				t.Fatalf("GetChannel: unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.childChannel, got, cmpopts.IgnoreFields(model.Channel{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("GetChannel mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHasTokens(t *testing.T) {
	t.Parallel()

	type tcase struct {
		expectTokens bool
	}

	tests := map[string]tcase{
		"expects_tokens": {
			expectTokens: true,
		},
		"expects_no_tokens": {
			expectTokens: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			if tc.expectTokens {
				rawToken, err := crypto.GenerateToken()
				if err != nil {
					t.Fatalf("GenerateToken: failed to generate token: %v", err)
				}

				hash := crypto.HashToken(rawToken)

				if err := store.NonTx().CreateToken(hash, model.RoleUser, 1, 1, 1, time.Now().Add(time.Hour)); err != nil {
					t.Fatalf("CreateToken: failed to create token: %v", err)
				}
			}

			hasTokens, err := store.NonTx().HasTokens()
			if err != nil {
				t.Fatalf("HasTokens: unexpected error: %v", err)
			}

			if hasTokens && !tc.expectTokens {
				t.Fatalf("HasTokens mismatch want=%t got=%t", tc.expectTokens, hasTokens)
			}
		})
	}
}

func TestCreateToken(t *testing.T) {
	t.Parallel()

	type tcase struct {
		hash         string
		role         model.Role
		channelScope int64
		createdBy    int64
		maxUses      int
		expiresAt    time.Time
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			hash:         crypto.HashToken("68FFA106C3C303C9BAB815240986C321"),
			role:         model.RoleUser,
			channelScope: 1,
			createdBy:    1,
			maxUses:      1,
			expiresAt:    time.Now().Add(time.Hour),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			if err := store.NonTx().CreateToken(tc.hash, tc.role, tc.channelScope, tc.createdBy, tc.maxUses, tc.expiresAt); err != nil {
				t.Fatalf("CreateToken: failed to create token: %v", err)
			}

			hasTokens, err := store.NonTx().HasTokens()
			if err != nil {
				t.Fatalf("HasTokens: unexpected error: %v", err)
			}
			if !hasTokens {
				t.Fatalf("HasTokens: expected tokens, but empty")
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	t.Parallel()

	type tcase struct {
		token *struct {
			hash         string
			role         model.Role
			channelScope int64
			createdBy    int64
			maxUses      int
			expiresAt    time.Time
		}
		expectValidation bool
	}

	tests := map[string]tcase{
		"valid_token": {
			token: &struct {
				hash         string
				role         model.Role
				channelScope int64
				createdBy    int64
				maxUses      int
				expiresAt    time.Time
			}{
				hash:         crypto.HashToken("68FFA106C3C303C9BAB815240986C321"),
				role:         model.RoleUser,
				channelScope: 1,
				createdBy:    1,
				maxUses:      10,
				expiresAt:    time.Now().Add(time.Hour),
			},
			expectValidation: true,
		},
		"invalid_token_expired": {
			token: &struct {
				hash         string
				role         model.Role
				channelScope int64
				createdBy    int64
				maxUses      int
				expiresAt    time.Time
			}{
				hash:         crypto.HashToken("68FFA106C3C303C9BAB815240986C321"),
				role:         model.RoleUser,
				channelScope: 1,
				createdBy:    1,
				maxUses:      10,
				expiresAt:    time.Now().Add(-time.Hour),
			},
			expectValidation: false,
		},
		"invalid_token_uses": {
			token: &struct {
				hash         string
				role         model.Role
				channelScope int64
				createdBy    int64
				maxUses      int
				expiresAt    time.Time
			}{
				hash:         crypto.HashToken("68FFA106C3C303C9BAB815240986C321"),
				role:         model.RoleUser,
				channelScope: 1,
				createdBy:    1,
				maxUses:      1,
				expiresAt:    time.Now().Add(time.Hour),
			},
			expectValidation: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			if err := store.NonTx().CreateToken(tc.token.hash, tc.token.role, tc.token.channelScope, tc.token.createdBy, tc.token.maxUses, tc.token.expiresAt); err != nil {
				t.Fatalf("CreateToken: failed to create token: %v", err)
			}

			// First validation
			tx1, err := store.Tx(ctx)
			if err != nil {
				t.Fatalf("Tx: %v", err)
			}
			_, err = tx1.ValidateToken(tc.token.hash)
			if err != nil && tc.expectValidation {
				t.Fatalf("ValidateToken_1: unexpected error: %v", err)
			}

			// Second validation — needs its own transaction
			tx2, err := store.Tx(ctx)
			if err != nil {
				t.Fatalf("Tx: %v", err)
			}
			_, err = tx2.ValidateToken(tc.token.hash)
			if !tc.expectValidation {
				if err == nil {
					t.Fatalf("ValidateToken_2: expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateToken: unexpected error: %v", err)
			}
		})
	}
}

func TestCreateBan(t *testing.T) {
	t.Parallel()

	type tcase struct {
		userID    int64
		ip        string
		reason    string
		bannedBy  int64
		expiredAt time.Time
	}

	tests := map[string]tcase{
		"minimum_required_fields": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  2,
			expiredAt: time.Now().Add(time.Hour),
		},
		"expired_ban": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  2,
			expiredAt: time.Now().Add(-time.Hour),
		},
		"ban_self": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  1,
			expiredAt: time.Now().Add(time.Hour),
		},
		"reason_empty": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "",
			bannedBy:  2,
			expiredAt: time.Now().Add(time.Hour),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			if err := store.NonTx().CreateBan(tc.userID, tc.ip, tc.reason, tc.bannedBy, tc.expiredAt); err != nil {
				t.Fatalf("CreateBan: unexpected error: %v", err)
			}
		})
	}
}

func TestIsUserBanned(t *testing.T) {
	t.Parallel()

	type tcase struct {
		userID    int64
		ip        string
		reason    string
		bannedBy  int64
		expiredAt time.Time
		expectBan bool
		multiBan  bool
	}

	tests := map[string]tcase{
		"user_has_valid_ban": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  2,
			expiredAt: time.Now().Add(time.Hour),
			expectBan: true,
			multiBan:  false,
		},
		"user_ban_expired": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  2,
			expiredAt: time.Now().Add(-time.Hour),
			expectBan: false,
			multiBan:  false,
		},
		"user_multi_ban": {
			userID:    1,
			ip:        "192.0.0.1",
			reason:    "Bad behavior",
			bannedBy:  2,
			expiredAt: time.Now().Add(-time.Hour),
			expectBan: true,
			multiBan:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			if err := store.NonTx().CreateBan(tc.userID, tc.ip, tc.reason, tc.bannedBy, tc.expiredAt); err != nil {
				t.Fatalf("CreateBan: unexpected error: %v", err)
			}
			// For the multi ban express the user has one valid and one invalid ban
			// asserting that the newer valid ban will still cause a positive ban
			if tc.multiBan {
				if err := store.NonTx().CreateBan(tc.userID, tc.ip, tc.reason, tc.bannedBy, time.Now().Add(time.Hour)); err != nil {
					t.Fatalf("CreateBan_Multi: unexpected error: %v", err)
				}
			}

			isBanned, err := store.NonTx().IsUserBanned(tc.userID)
			if err != nil {
				t.Fatalf("IsUserBanned: unexpected error: %v", err)
			}
			if tc.expectBan != isBanned {
				t.Fatalf("IsUserBanned: ban mismatch want=%t got=%t", tc.expectBan, isBanned)
			}
		})
	}
}

func TestCreateMessage(t *testing.T) {
	t.Parallel()

	type tcase struct {
		message   *model.Message
		expectErr bool
	}

	tests := map[string]tcase{
		"valid_message": {
			message: &model.Message{
				ChannelID: 1,
				SenderID:  1,
				Body:      "Hello, world!",
			},
			expectErr: false,
		},
		"empty_body": {
			message: &model.Message{
				ChannelID: 1,
				SenderID:  1,
				Body:      "",
			},
			expectErr: true,
		},
		"whitespace_only_body": {
			message: &model.Message{
				ChannelID: 1,
				SenderID:  1,
				Body:      "   ",
			},
			expectErr: true,
		},
		"body_at_max_length": {
			message: &model.Message{
				ChannelID: 1,
				SenderID:  1,
				Body:      strings.Repeat("a", model.MessageMaxBodyLength),
			},
			expectErr: false,
		},
		"body_exceeds_max_length": {
			message: &model.Message{
				ChannelID: 1,
				SenderID:  1,
				Body:      strings.Repeat("a", model.MessageMaxBodyLength+1),
			},
			expectErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, err := NewTestSqlConn(t)
			if err != nil {
				t.Fatalf("failed to open test connection: %v", err)
			}

			err = store.NonTx().CreateMessage(tc.message)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("CreateMessage: expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateMessage: unexpected error: %v", err)
			}

			if tc.message.ID == 0 {
				t.Fatalf("CreateMessage: expected non-zero ID")
			}
		})
	}
}

func TestListMessages(t *testing.T) {
	t.Parallel()

	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("failed to open test connection: %v", err)
	}

	st := store.NonTx()

	msgs := []model.Message{
		{ChannelID: 1, SenderID: 1, Body: "msg one"},
		{ChannelID: 1, SenderID: 2, Body: "msg two"},
		{ChannelID: 2, SenderID: 1, Body: "msg three"},
	}
	for i := range msgs {
		if err := st.CreateMessage(&msgs[i]); err != nil {
			t.Fatalf("CreateMessage[%d]: unexpected error: %v", i, err)
		}
	}

	t.Run("all_messages", func(t *testing.T) {
		got, err := st.ListMessages(model.MessageFilters{})
		if err != nil {
			t.Fatalf("ListMessages: unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ListMessages: expected 3 messages, got %d", len(got))
		}
	})

	t.Run("filter_by_channel", func(t *testing.T) {
		channelID := int64(1)
		got, err := st.ListMessages(model.MessageFilters{LimitToChannelID: &channelID})
		if err != nil {
			t.Fatalf("ListMessages: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ListMessages: expected 2 messages for channel 1, got %d", len(got))
		}
	})

	t.Run("filter_by_sender", func(t *testing.T) {
		senderID := int64(2)
		got, err := st.ListMessages(model.MessageFilters{LimitToSenderID: &senderID})
		if err != nil {
			t.Fatalf("ListMessages: unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("ListMessages: expected 1 message for sender 2, got %d", len(got))
		}
	})

	t.Run("pagination", func(t *testing.T) {
		pageSize := int64(2)
		got, err := st.ListMessages(model.MessageFilters{PageSize: &pageSize})
		if err != nil {
			t.Fatalf("ListMessages: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ListMessages: expected 2 messages with page size 2, got %d", len(got))
		}
	})

	t.Run("offset", func(t *testing.T) {
		pageSize := int64(10)
		offset := int64(2)
		got, err := st.ListMessages(model.MessageFilters{PageSize: &pageSize, Offset: &offset})
		if err != nil {
			t.Fatalf("ListMessages: unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("ListMessages: expected 1 message with offset 2, got %d", len(got))
		}
	})
}

func TestDeleteMessage(t *testing.T) {
	t.Parallel()

	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("failed to open test connection: %v", err)
	}

	st := store.NonTx()

	msg := &model.Message{ChannelID: 1, SenderID: 1, Body: "to be deleted"}
	if err := st.CreateMessage(msg); err != nil {
		t.Fatalf("CreateMessage: unexpected error: %v", err)
	}

	if err := st.DeleteMessage(msg.ID); err != nil {
		t.Fatalf("DeleteMessage: unexpected error: %v", err)
	}

	got, err := st.ListMessages(model.MessageFilters{})
	if err != nil {
		t.Fatalf("ListMessages: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("DeleteMessage: expected 0 messages after delete, got %d", len(got))
	}
}
