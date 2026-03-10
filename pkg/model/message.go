package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MessageMaxBodyLength = 256

var ErrMessageBodyTooLong = fmt.Errorf("message body exceeds %d characters", MessageMaxBodyLength)
var ErrMessageBodyEmpty = errors.New("message body cannot be empty")

type Message struct {
	ID        int64     `json:"id"`
	ChannelID int64     `json:"channel_id"`
	SenderID  int64     `json:"sender_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func (m *Message) Validate() error {
	if strings.TrimSpace(m.Body) == "" {
		return ErrMessageBodyEmpty
	} else if utf8.RuneCountInString(m.Body) > MessageMaxBodyLength {
		return ErrMessageBodyTooLong
	}

	return nil
}

type MessageFilters struct {
	LimitToChannelID *int64
	LimitToSenderID  *int64
	PageSize         *int64
	Offset           *int64
}
