package ui

import (
	"fmt"
	"time"

	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func banListEntryText(ban pb.BanInfo) string {
	target := fmt.Sprintf("Account #%d", ban.UserID)
	if ban.Username != "" {
		target = "Account: " + ban.Username
	}
	if ban.IP != "" {
		target = "Exact IP: " + ban.IP
	}

	expiry := "permanent"
	if ban.ExpiresAt > 0 {
		expiry = time.Unix(ban.ExpiresAt, 0).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("%s - %s", target, expiry)
}
