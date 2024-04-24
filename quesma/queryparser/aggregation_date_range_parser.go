package queryparser

import (
	"mitmproxy/quesma/logger"
	"mitmproxy/quesma/model/bucket_aggregations"
	"unicode"
)

func (cw *ClickhouseQueryTranslator) parseDateRangeAggregation(dateRange QueryMap) bucket_aggregations.DateRange {
	var fieldName string
	if field, exists := dateRange["field"]; exists {
		if fieldNameRaw, ok := field.(string); ok {
			fieldName = cw.Table.ResolveField(fieldNameRaw)
		} else {
			logger.WarnWithCtx(cw.Ctx).Msgf("field specified for date range aggregation is not a string. Using empty. Querymap: %v", dateRange)
		}
	} else {
		logger.WarnWithCtx(cw.Ctx).Msgf("no field specified for date range aggregation. Using empty. Querymap: %v", dateRange)
	}
	var ranges []any
	var ok bool
	if rangesRaw, exists := dateRange["ranges"]; exists {
		if ranges, ok = rangesRaw.([]any); !ok {
			logger.WarnWithCtx(cw.Ctx).Msgf("ranges specified for date range aggregation is not an array. Using empty. Querymap: %v", dateRange)
		}
	} else {
		logger.WarnWithCtx(cw.Ctx).Msgf("no ranges specified for date range aggregation. Using empty. Querymap: %v", dateRange)
	}
	intervals := make([]bucket_aggregations.DateTimeInterval, 0, len(ranges))
	selectColumnsNr := len(ranges) // we query Clickhouse for every unbounded part of interval (begin and end)
	for _, Range := range ranges {
		rangeMap := Range.(QueryMap)
		var intervalBegin, intervalEnd string
		from, exists := rangeMap["from"]
		if exists {
			if fromRaw, ok := from.(string); ok {
				intervalBegin = cw.parseDateTimeInClickhouseMathLanguage(fromRaw)
				selectColumnsNr++
			} else {
				logger.WarnWithCtx(cw.Ctx).Msgf("from specified for date range aggregation is not a string. Querymap: %v "+
					"Using default (unbounded).", dateRange)
				intervalBegin = bucket_aggregations.UnboundedInterval
			}
		} else {
			intervalBegin = bucket_aggregations.UnboundedInterval
		}
		to, exists := rangeMap["to"]
		if exists {
			if toRaw, ok := to.(string); ok {
				intervalEnd = cw.parseDateTimeInClickhouseMathLanguage(toRaw)
				selectColumnsNr++
			} else {
				logger.WarnWithCtx(cw.Ctx).Msgf("To specified for date range aggregation is not a string. Querymap: %v "+
					"Using default (unbounded).", dateRange)
				intervalEnd = bucket_aggregations.UnboundedInterval
			}
		} else {
			intervalEnd = bucket_aggregations.UnboundedInterval
		}
		intervals = append(intervals, bucket_aggregations.NewDateTimeInterval(intervalBegin, intervalEnd))
	}
	return bucket_aggregations.NewDateRange(cw.Ctx, fieldName, intervals, selectColumnsNr)
}

// parseDateTimeInClickhouseMathLanguage parses dateTime from Clickhouse's format
// It's described here: https://www.elastic.co/guide/en/elasticsearch/reference/current/search-aggregations-bucket-daterange-aggregation.html
// Maybe not 100% of it is implemented, not sure.
func (cw *ClickhouseQueryTranslator) parseDateTimeInClickhouseMathLanguage(dateTime string) string {
	// So far we've seen only either:
	// 1. 2024-01-01 format
	if cw.isSimpleDate(dateTime) {
		return "'" + dateTime + "'"
	}
	// 2. expressions like now() or now()-1d
	parsedWithoutRounding := parseDateMathExpression(dateTime)
	return cw.addRoundingToClickhouseDateTime(dateTime, parsedWithoutRounding)
}

// isSimpleDate returns true if the given dateTime is a simple date string in format 2024-04-15
func (cw *ClickhouseQueryTranslator) isSimpleDate(dateTime string) bool {
	if len(dateTime) != len("2024-04-15") {
		return false
	}
	for _, idx := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
		if !unicode.IsDigit(rune(dateTime[idx])) {
			return false
		}
	}
	for _, idx := range []int{4, 7} {
		if dateTime[idx] != '-' {
			return false
		}
	}
	return true
}

// addRoundingToClickhouseDateTime adds rounding that might be present in Clickhouse's format.
// If it exists, is at the end of dateTime in a "/[char]" format, e.g. /d, /w, /M, /Y
// It means, e.g. /M, that we want to round to the beginning of the month.
// It's done via Clickhouse's functions toStartOfDay, toStartOfWeek, toStartOfMonth, toStartOfYear.
func (cw *ClickhouseQueryTranslator) addRoundingToClickhouseDateTime(dateTime string, parsedWithoutRounding string) string {
	const defaultRounding = 'd'
	var roundingFunction = map[rune]string{
		'd': "toStartOfDay",
		'w': "toStartOfWeek",
		'M': "toStartOfMonth",
		'Y': "toStartOfYear",
	}
	if len(dateTime) < len("/d") || dateTime[len(dateTime)-2] != '/' {
		return parsedWithoutRounding
	}
	switch dateTime[len(dateTime)-1] {
	case 'd', 'w', 'M', 'Y':
		return roundingFunction[rune(dateTime[len(dateTime)-1])] + "(" + parsedWithoutRounding + ")"
	default:
		logger.Error().Msgf("Unknown rounding character %c in dateTime %s. Defaulting to /%s", dateTime[len(dateTime)-1], dateTime, string(defaultRounding))
		return roundingFunction[defaultRounding] + "(" + parsedWithoutRounding + ")"
	}
}
