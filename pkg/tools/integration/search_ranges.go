package integrationtools

import (
	"fmt"
	"strings"
	"time"

	kagiopenapi "github.com/kagisearch/kagi-openapi-golang"
)

func applySogouRangeHint(query string, rangeCode string) string {
	switch rangeCode {
	case "d":
		return query + " 最近一天"
	case "w":
		return query + " 最近一周"
	case "m":
		return query + " 最近一个月"
	case "y":
		return query + " 最近一年"
	default:
		return query
	}
}

func normalizeSearchRange(raw string) (string, error) {
	rangeCode := strings.ToLower(strings.TrimSpace(raw))
	switch rangeCode {
	case "", "d", "w", "m", "y":
		return rangeCode, nil
	default:
		return "", fmt.Errorf("range must be one of: d, w, m, y")
	}
}

func mapBraveFreshness(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "pd"
	case "w":
		return "pw"
	case "m":
		return "pm"
	case "y":
		return "py"
	default:
		return ""
	}
}

func mapTavilyTimeRange(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapPerplexityRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapDuckDuckGoDateFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "d"
	case "w":
		return "w"
	case "m":
		return "m"
	case "y":
		return "t"
	default:
		return ""
	}
}

func mapSearXNGTimeRange(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapGLMRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "oneDay"
	case "w":
		return "oneWeek"
	case "m":
		return "oneMonth"
	case "y":
		return "oneYear"
	default:
		return "noLimit"
	}
}

func mapBaiduRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d", "w":
		// Baidu does not expose a day-level filter. Use the closest supported
		// window to keep recency bias instead of silently dropping the filter.
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapKagiLensTimeFilter(rangeCode string, now time.Time) *kagiopenapi.SearchRequestLens {
	lens := kagiopenapi.NewSearchRequestLens()
	switch rangeCode {
	case "d":
		lens.SetTimeRelative("day")
	case "w":
		lens.SetTimeRelative("week")
	case "m":
		lens.SetTimeRelative("month")
	case "y":
		lens.SetTimeAfter(now.AddDate(-1, 0, 0).Format("2006-01-02"))
	default:
		return nil
	}
	return lens
}
