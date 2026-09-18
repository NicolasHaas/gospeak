package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxBanDuration = 10 * 365 * 24 * time.Hour

func banDurationSeconds(input string, useMaximum bool) (int64, error) {
	if useMaximum {
		return int64(maxBanDuration / time.Second), nil
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return 0, errors.New("enter a duration")
	}
	duration, err := time.ParseDuration(input)
	if err != nil {
		return 0, errors.New("use a duration such as 30m, 1h, or 24h")
	}
	if duration <= 0 {
		return 0, errors.New("duration must be greater than zero")
	}
	if duration%time.Second != 0 {
		return 0, errors.New("duration must use whole seconds")
	}
	if duration > maxBanDuration {
		return 0, fmt.Errorf("duration cannot exceed 10 years (%s)", maxBanDuration)
	}
	return int64(duration / time.Second), nil
}
