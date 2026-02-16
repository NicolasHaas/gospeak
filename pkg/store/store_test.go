package store_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/store"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func NewTestSqlConn(t *testing.T) (*store.Store, error) {
	t.Helper()

	// Creates a temporary in-memory datastore
	// with a unique name per-test
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := store.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("store_test: failed to open db: %w", err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			fmt.Printf("Error closing database: %v\n", err)
		}
	})

	return store, nil
}

func TestZeroTime(t *testing.T) {
	store, err := NewTestSqlConn(t)
	if err != nil {
		t.Fatalf("failed to open test connection: %v", err)
	}

	if diff := cmp.Diff(time.Time{}, store.ZeroTime()); diff != "" {
		t.Errorf("store.ZeroTime mismatch (-want +got):\\n%s", diff)
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

			got, err := store.CreateUser(tc.username, tc.role)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := &model.User{
				Username: tc.username,
				Role:     tc.role,
			}

			if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(model.User{}, "ID", "CreatedAt")); diff != "" {
				t.Errorf("store.CreateUser mismatch (-want +got):\\n%s", diff)
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
				u, err := store.CreateUser(tc.username, tc.role)
				if err != nil {
					t.Fatalf("failed to seed user: %v", err)
				}
				seeded = u
			}

			got, err := store.GetUserByUsername(tc.username)

			if !tc.expectUser {
				if got != nil {
					t.Fatalf("expected nil, got user")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
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

	_, err = store.CreateUser("johndoe", model.RoleUser)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	res, err := store.GetUserByID(want)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

			u, err := store.CreateUser(tc.username, tc.role)
			if err != nil {
				t.Fatalf("failed to seed user: %v", err)
			}

			if err := store.UpdateUserRole(u.ID, model.RoleAdmin); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := &model.User{
				Username: tc.username,
				Role:     model.RoleAdmin,
			}

			got, err := store.GetUserByID(u.ID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
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
				_, err := store.CreateUser(user.Username, user.Role)
				if err != nil {
					t.Fatalf("failed to seed user: %v", err)
				}
			}

			users, err := store.ListUsers()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.users, users, cmpopts.IgnoreFields(model.User{}, "ID", "CreatedAt")); diff != "" {
				t.Fatalf("ListUsers mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
