package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultExportWithin = "30d"

// parseWithin accepts a day count such as "30d", any Go duration such as
// "12h", or "all"/"0" for no limit, which returns zero.
func parseWithin(value string) (time.Duration, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "all" || value == "0" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil || count <= 0 {
			return 0, fmt.Errorf("invalid --within %q: use a positive day count such as 30d, a duration such as 12h, or all", value)
		}
		return time.Duration(count) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid --within %q: use a positive day count such as 30d, a duration such as 12h, or all", value)
	}
	return duration, nil
}
