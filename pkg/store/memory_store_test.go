package store_test

import (
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/store"
)

func TestStoreBasicFlow(t *testing.T) {
	withStores(t, func(t *testing.T, st store.DataStore) {
		user, err := st.CreateUser("johndoe", model.RoleUser)
		if err != nil {
			t.Fatalf("CreateUser: unexpected error: %v", err)
		}
		if user.ID == 0 {
			t.Fatalf("CreateUser: expected non-zero ID")
		}

		fetched, err := st.GetUserByID(user.ID)
		if err != nil {
			t.Fatalf("GetUserByID: unexpected error: %v", err)
		}
		if fetched == nil || fetched.ID != user.ID {
			t.Fatalf("GetUserByID: expected user with ID %d", user.ID)
		}

		rawToken, err := crypto.GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: unexpected error: %v", err)
		}
		hash := crypto.HashToken(rawToken)
		if err := st.CreateToken(hash, model.RoleUser, 0, user.ID, 1, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CreateToken: unexpected error: %v", err)
		}

		role, err := st.ValidateToken(hash)
		if err != nil {
			t.Fatalf("ValidateToken: unexpected error: %v", err)
		}
		if role != model.RoleUser {
			t.Fatalf("ValidateToken: role mismatch want=%d got=%d", model.RoleUser, role)
		}
	})
}
