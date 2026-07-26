package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sizeRegex = regexp.MustCompile(`^(\d+)\s*(B|KB|MB|GB|TB|K|M|G|T)?$`)

// ParseTTL converts human-friendly TTL strings to seconds.
// Supports: "30d", "24h", "90m", "0" (permanent), and Go duration format.
func ParseTTL(ttl string) (int64, error) {
	cleaned := strings.ToLower(strings.TrimSpace(ttl))
	if cleaned == "0" {
		return 0, nil
	}
	if strings.HasSuffix(cleaned, "d") {
		daysStr := strings.TrimSuffix(cleaned, "d")
		days, err := strconv.ParseInt(daysStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day format for TTL: %s", ttl)
		}
		return days * 24 * 3600, nil
	}
	dur, err := time.ParseDuration(ttl)
	if err != nil {
		return 0, fmt.Errorf("failed to parse TTL: %w", err)
	}
	return int64(dur.Seconds()), nil
}

// ParseSize parses a human-readable size string to bytes.
func ParseSize(sizeStr string) (int64, error) {
	sizeStr = strings.ToUpper(strings.TrimSpace(sizeStr))
	if sizeStr == "" {
		return 0, nil
	}
	matches := sizeRegex.FindStringSubmatch(sizeStr)
	if len(matches) < 2 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}
	val, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := "B"
	if len(matches) > 2 && matches[2] != "" {
		unit = matches[2]
	}
	switch unit {
	case "K", "KB":
		return val * 1024, nil
	case "M", "MB":
		return val * 1024 * 1024, nil
	case "G", "GB":
		return val * 1024 * 1024 * 1024, nil
	case "T", "TB":
		return val * 1024 * 1024 * 1024 * 1024, nil
	default:
		return val, nil
	}
}
