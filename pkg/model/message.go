package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MessageMaxBodyLength = 2000

var ErrMessageBodyTooLong = fmt.Errorf("message body exceeds %d characters", MessageMaxBodyLength)
var ErrMessageBodyEmpty = errors.New("message body cannot be empty")
var ErrMessageBodyInvalidUTF8 = errors.New("message body is not valid UTF-8")
var ErrMessageBodyControl = errors.New("message body contains control or format characters")

type Message struct {
	ID         int64     `json:"id"`
	ChannelID  int64     `json:"channel_id"`
	SenderID   int64     `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

func (m *Message) Validate() error {
	if strings.TrimSpace(m.Body) == "" {
		return ErrMessageBodyEmpty
	} else if !utf8.ValidString(m.Body) {
		return ErrMessageBodyInvalidUTF8
	} else if containsControlCharacter(m.Body) {
		return ErrMessageBodyControl
	} else if utf8.RuneCountInString(m.Body) > MessageMaxBodyLength {
		return ErrMessageBodyTooLong
	}

	return nil
}

type MessageFilters struct {
	LimitToChannelID *int64
	PageSize         *int64
	BeforeID         int64
	Since            time.Time
}
