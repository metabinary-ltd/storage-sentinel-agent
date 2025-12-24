package scheduler

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ParseInterval parses an interval string (e.g., "6h", "30m", "1d") and returns the duration
func ParseInterval(interval string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d+)([smhd])$`)
	matches := re.FindStringSubmatch(interval)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid interval format: %s (expected format: number+unit, e.g., 6h, 30m, 1d)", interval)
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid interval value: %w", err)
	}

	unit := matches[2]
	var duration time.Duration

	switch unit {
	case "s":
		duration = time.Duration(value) * time.Second
	case "m":
		duration = time.Duration(value) * time.Minute
	case "h":
		duration = time.Duration(value) * time.Hour
	case "d":
		duration = time.Duration(value) * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid interval unit: %s (expected: s, m, h, d)", unit)
	}

	return duration, nil
}

// NextCronTime calculates the next execution time for a cron expression
// Supports standard 5-field cron: minute hour day month weekday
// This is a simplified parser - for production use, consider using github.com/robfig/cron/v3
func NextCronTime(cronExpr string, from time.Time) (time.Time, error) {
	parts := regexp.MustCompile(`\s+`).Split(cronExpr, -1)
	if len(parts) != 5 {
		return time.Time{}, fmt.Errorf("invalid cron expression: %s (expected 5 fields: minute hour day month weekday)", cronExpr)
	}

	minute, hour, day, month, weekday := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Start from the next minute
	next := from.Truncate(time.Minute).Add(time.Minute)

	// Try to find next valid time within the next year
	maxAttempts := 365 * 24 * 60 // Max attempts: 1 year in minutes
	for i := 0; i < maxAttempts; i++ {
		// Convert weekday: Go uses 0-6 (Sunday=0), cron uses 0-7 (0 and 7 are Sunday)
		weekdayInt := int(next.Weekday())
		weekdayStr := strconv.Itoa(weekdayInt)
		// Also check if 7 matches (Sunday in cron)
		weekdayMatches := matchesCronField(weekdayStr, weekday) || (weekdayInt == 0 && matchesCronField("7", weekday))
		
		if matchesCronField(strconv.Itoa(next.Minute()), minute) &&
			matchesCronField(strconv.Itoa(next.Hour()), hour) &&
			matchesCronField(strconv.Itoa(next.Day()), day) &&
			matchesCronField(strconv.Itoa(int(next.Month())), month) &&
			weekdayMatches {
			return next, nil
		}
		next = next.Add(time.Minute)
	}

	return time.Time{}, fmt.Errorf("could not find next execution time for cron: %s", cronExpr)
}

// matchesCronField checks if a value matches a cron field pattern
func matchesCronField(value, pattern string) bool {
	if pattern == "*" {
		return true
	}

	// Handle exact match
	if value == pattern {
		return true
	}

	// Handle ranges (e.g., "1-5")
	if re := regexp.MustCompile(`^(\d+)-(\d+)$`); re.MatchString(pattern) {
		matches := re.FindStringSubmatch(pattern)
		start, _ := strconv.Atoi(matches[1])
		end, _ := strconv.Atoi(matches[2])
		val, _ := strconv.Atoi(value)
		return val >= start && val <= end
	}

	// Handle lists (e.g., "1,3,5")
	if re := regexp.MustCompile(`^(\d+)(,\d+)*$`); re.MatchString(pattern) {
		parts := regexp.MustCompile(`,`).Split(pattern, -1)
		for _, part := range parts {
			if part == value {
				return true
			}
		}
	}

	// Handle step values (e.g., "*/5" or "0-30/5")
	if re := regexp.MustCompile(`^(.+)/(\d+)$`); re.MatchString(pattern) {
		matches := re.FindStringSubmatch(pattern)
		base := matches[1]
		step, _ := strconv.Atoi(matches[2])
		val, _ := strconv.Atoi(value)

		if base == "*" {
			return val%step == 0
		}
		// For ranges with steps, check if value is in range and matches step
		if rangeRe := regexp.MustCompile(`^(\d+)-(\d+)$`); rangeRe.MatchString(base) {
			rangeMatches := rangeRe.FindStringSubmatch(base)
			start, _ := strconv.Atoi(rangeMatches[1])
			end, _ := strconv.Atoi(rangeMatches[2])
			if val >= start && val <= end {
				return (val-start)%step == 0
			}
		}
	}

	return false
}

// ParseScheduleValue parses either an interval or cron expression and returns the next execution time
func ParseScheduleValue(scheduleType, scheduleValue string, from time.Time) (time.Time, error) {
	if scheduleType == "INTERVAL" {
		duration, err := ParseInterval(scheduleValue)
		if err != nil {
			return time.Time{}, err
		}
		return from.Add(duration), nil
	} else if scheduleType == "CRON" {
		return NextCronTime(scheduleValue, from)
	}
	return time.Time{}, fmt.Errorf("unknown schedule type: %s", scheduleType)
}

