package datastore

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/model"
)

func TestRetentionRejectsMissingDatabase(t *testing.T) {
	for _, st := range []DataStore{(ProviderFactory{}).NonTx(), &nonTxProvider{}} {
		if err := st.CreateMessageWithRetention(&model.Message{}, 1, 0); err == nil {
			t.Fatal("missing database accepted")
		}
	}
}
