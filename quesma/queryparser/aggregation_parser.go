package queryparser

import (
	"context"
	"encoding/json"
	"fmt"
	"mitmproxy/quesma/logger"
	"mitmproxy/quesma/model"
	"mitmproxy/quesma/model/bucket_aggregations"
	"mitmproxy/quesma/model/metrics_aggregations"
	"slices"
	"strconv"
	"strings"
)

type aggrQueryBuilder struct {
	model.QueryWithAggregation
	whereBuilder SimpleQuery // during building this is used for where clause, not `aggr.Where`
	ctx          context.Context
}

type metricsAggregation struct {
	AggrType    string
	FieldNames  []string           // on these fields we're doing aggregation. Array, because e.g. 'top_hits' can have multiple fields
	Percentiles map[string]float64 // Only for percentiles aggregation
	SortBy      string             // Only for top_metrics
	Size        int                // Only for top_metrics
	Order       string             // Only for top_metrics
}

func (b *aggrQueryBuilder) buildAggregationCommon() model.QueryWithAggregation {
	query := b.QueryWithAggregation
	query.WhereClause = b.whereBuilder.Sql.Stmt

	// Need to copy, as we might be proceeding to modify 'b' pointer
	query.CopyAggregationFields(b.QueryWithAggregation)
	if len(query.Fields) > 0 && query.Fields[len(query.Fields)-1] == model.EmptyFieldSelection { // TODO 99% sure it's removed in next PR, let's leave for now
		query.Fields = query.Fields[:len(query.Fields)-1]
	}
	query.RemoveEmptyGroupBy()
	query.TrimKeywordFromFields()
	return query
}

func (b *aggrQueryBuilder) buildCountAggregation() model.QueryWithAggregation {
	query := b.buildAggregationCommon()
	query.Type = metrics_aggregations.Count{}
	query.NonSchemaFields = append(query.NonSchemaFields, "count()")
	return query
}

func (b *aggrQueryBuilder) buildBucketAggregation() model.QueryWithAggregation {
	query := b.buildAggregationCommon()
	query.NonSchemaFields = append(query.NonSchemaFields, "count()")
	return query
}
func (b *aggrQueryBuilder) buildMetricsAggregation(metricsAggr metricsAggregation) model.QueryWithAggregation {
	query := b.buildAggregationCommon()
	switch metricsAggr.AggrType {
	case "sum", "min", "max", "avg":
		query.NonSchemaFields = append(query.NonSchemaFields, metricsAggr.AggrType+`("`+metricsAggr.FieldNames[0]+`")`)
	case "quantile":
		for usersPercent, percentAsFloat := range metricsAggr.Percentiles {
			query.NonSchemaFields = append(query.NonSchemaFields, fmt.Sprintf("quantiles(%6f)(`%s`) AS `quantile_%s`", percentAsFloat, metricsAggr.FieldNames[0], usersPercent))
		}
	case "cardinality":
		query.NonSchemaFields = append(query.NonSchemaFields, `COUNT(DISTINCT "`+metricsAggr.FieldNames[0]+`")`)
	case "value_count":
		query.NonSchemaFields = append(query.NonSchemaFields, "count()")
	case "stats":
		query.NonSchemaFields = append(
			query.NonSchemaFields,
			"count(`"+metricsAggr.FieldNames[0]+"`)",
			"min(`"+metricsAggr.FieldNames[0]+"`)",
			"max(`"+metricsAggr.FieldNames[0]+"`)",
			"avg(`"+metricsAggr.FieldNames[0]+"`)",
			"sum(`"+metricsAggr.FieldNames[0]+"`)",
		)
	case "top_hits":
		query.Fields = append(query.Fields, metricsAggr.FieldNames...)
		fieldsAsString := strings.Join(metricsAggr.FieldNames, ", ")
		query.FromClause = fmt.Sprintf(
			"(SELECT %s, ROW_NUMBER() OVER (PARTITION BY %s) AS %s FROM %s)",
			fieldsAsString, fieldsAsString, model.RowNumberColumnName, query.FromClause,
		)
	case "top_metrics":
		// This appending of `metricsAggr.SortBy` and having it duplicated in SELECT block
		// is a way to pass value we're sorting by to the query result. In the future we might add SQL aliasing support, e.g. SELECT x AS 'sort_by' FROM ...
		if len(b.QueryWithAggregation.Query.GroupByFields) > 0 {
			var ordFunc string
			switch metricsAggr.Order {
			case "asc":
				ordFunc = `MAX`
			case "desc":
				ordFunc = `MIN`
			}
			var topSelectFields []string
			innerFields := append(metricsAggr.FieldNames, metricsAggr.SortBy)
			for _, field := range innerFields {
				topSelectFields = append(topSelectFields, fmt.Sprintf(`%s("%s") AS "windowed_%s"`, ordFunc, field, field))
			}
			query.NonSchemaFields = append(query.NonSchemaFields, topSelectFields...)
			partitionBy := strings.Join(b.QueryWithAggregation.Query.GroupByFields, "")
			fieldsAsString := strings.Join(quoteArray(innerFields), ", ") // need those fields in the inner clause
			query.FromClause = fmt.Sprintf(
				"(SELECT %s, ROW_NUMBER() OVER (PARTITION BY %s ORDER BY %s %s) AS %s FROM %s WHERE %s)",
				fieldsAsString, partitionBy,
				strconv.Quote(metricsAggr.SortBy), metricsAggr.Order,
				model.RowNumberColumnName, query.FromClause, b.whereBuilder.Sql.Stmt,
			)
			query.WhereClause = query.WhereClause + fmt.Sprintf(" AND %s <= %d", model.RowNumberColumnName, metricsAggr.Size)
		} else {
			query.Fields = append(metricsAggr.FieldNames, metricsAggr.SortBy)
			query.SuffixClauses = append(query.SuffixClauses,
				fmt.Sprintf(`ORDER BY %s %s LIMIT %d`, metricsAggr.SortBy, metricsAggr.Order, metricsAggr.Size))
		}
	case "percentile_ranks":
		fieldName := metricsAggr.FieldNames[0]
		for _, cutValueAsString := range metricsAggr.FieldNames[1:] {
			cutValue, _ := strconv.ParseFloat(cutValueAsString, 64)
			Select := fmt.Sprintf("count(if(%s<=%f, 1, NULL))/count(*)*100", strconv.Quote(fieldName), cutValue)
			query.NonSchemaFields = append(query.NonSchemaFields, Select)
		}
	default:
		logger.WarnWithCtx(b.ctx).Msgf("unknown metrics aggregation: %s", metricsAggr.AggrType)
		query.CanParse = false
	}
	switch metricsAggr.AggrType {
	case "sum":
		query.Type = metrics_aggregations.Sum{}
	case "min":
		query.Type = metrics_aggregations.Min{}
	case "max":
		query.Type = metrics_aggregations.Max{}
	case "avg":
		query.Type = metrics_aggregations.Avg{}
	case "stats":
		query.Type = metrics_aggregations.Stats{}
	case "cardinality":
		query.Type = metrics_aggregations.Cardinality{}
	case "quantile":
		query.Type = metrics_aggregations.Quantile{}
	case "top_hits":
		query.Type = metrics_aggregations.TopHits{}
	case "top_metrics":
		query.Type = metrics_aggregations.TopMetrics{}
	case "value_count":
		query.Type = metrics_aggregations.ValueCount{}
	case "percentile_ranks":
		query.Type = metrics_aggregations.PercentileRanks{}
	}
	return query
}

// ParseAggregationJson parses JSON with aggregation query and returns array of queries with aggregations.
// If there are no aggregations, returns nil.
func (cw *ClickhouseQueryTranslator) ParseAggregationJson(queryAsJson string) ([]model.QueryWithAggregation, error) {
	queryAsMap := make(QueryMap)
	err := json.Unmarshal([]byte(queryAsJson), &queryAsMap)
	if err != nil {
		return nil, fmt.Errorf("unmarshal error: %v", err)
	}
	currentAggr := aggrQueryBuilder{}
	currentAggr.FromClause = cw.Table.FullTableName()
	currentAggr.ctx = cw.Ctx
	if queryPart, ok := queryAsMap["query"]; ok {
		currentAggr.whereBuilder = cw.parseQueryMap(queryPart.(QueryMap))
	}

	// COUNT(*) is needed for every request. We should change it and don't duplicate it, as some
	// requests also ask for that themselves, but let's leave it for later.
	aggregations := []model.QueryWithAggregation{currentAggr.buildCountAggregation()}

	if aggs, ok := queryAsMap["aggs"]; ok {
		// The 'for' below duplicates the logic of parseAggregation a little bit, but let's refactor that later.
		// Duplication is needed, because one request's most outer aggregator's name is "sampler", which
		// is the same as the name of one bucket aggregation, and parsing algorithm mishandles the aggregator name
		// for bucket aggregation name...
		for aggrName, aggr := range aggs.(QueryMap) {
			currentAggr.Aggregators = append(currentAggr.Aggregators, model.NewAggregatorEmpty(aggrName))
			cw.parseAggregation(&currentAggr, aggr.(QueryMap), &aggregations)
			currentAggr.Aggregators = currentAggr.Aggregators[:len(currentAggr.Aggregators)-1]
		}
	} else {
		return nil, fmt.Errorf("no aggs")
	}

	return aggregations, nil
}

// 'resultAccumulator' - array when we store results
// Builds aggregations recursively. Seems to be working on all examples so far,
// even though it's a pretty simple algorithm.
// When making changes, look at the order in which we parse fields, it is very important for correctness.
func (cw *ClickhouseQueryTranslator) parseAggregation(currentAggr *aggrQueryBuilder, queryMap QueryMap, resultAccumulator *[]model.QueryWithAggregation) {
	if len(queryMap) == 0 {
		return
	}

	filterOnThisLevel := false
	whereBeforeNesting := currentAggr.whereBuilder // to restore it after processing this level
	queryTypeBeforeNesting := currentAggr.Type

	// 1. Metrics aggregation => always leaf
	metricsAggrResult, ok := cw.tryMetricsAggregation(queryMap)
	if ok {
		*resultAccumulator = append(*resultAccumulator, currentAggr.buildMetricsAggregation(metricsAggrResult))
		return
	}

	// 2. Now process filter(s) first, because they apply to everything else on the same level or below.
	// Also filter introduces count to current level.
	if filter, ok := queryMap["filter"]; ok {
		filterOnThisLevel = true
		currentAggr.Type = metrics_aggregations.Count{}
		currentAggr.whereBuilder = cw.combineWheres(
			currentAggr.whereBuilder,
			cw.parseQueryMap(filter.(QueryMap)),
		)
		*resultAccumulator = append(*resultAccumulator, currentAggr.buildCountAggregation())
		delete(queryMap, "filter")
	}

	// Filters is pretty wild, wildest from any aggregations we handle so far.
	if filters, ok := queryMap["filters"]; ok {
		// TODO add filters!!!
		filterOnThisLevel = true
		_ = filters
		// cw.parseAggregation(currentAggr, filters.(QueryMap)["filters"].(QueryMap), resultAccumulator)
		delete(queryMap, "filters")
	}

	// 3. Bucket aggregations. They introduce new subaggregations, even if no explicit subaggregation defined on this level.
	bucketAggrPresent, addedNonSchemaField := cw.tryBucketAggregation(currentAggr, queryMap)
	if addedNonSchemaField {
		currentAggr.Aggregators[len(currentAggr.Aggregators)-1].Empty = false
	}

	if aggs, ok := queryMap["aggs"]; ok {
		cw.parseAggregation(currentAggr, aggs.(QueryMap), resultAccumulator)
		delete(queryMap, "aggs")
	}

	if bucketAggrPresent {
		*resultAccumulator = append(*resultAccumulator, currentAggr.buildBucketAggregation())
	}

	// 5. At the end, we process subaggregations, introduced via (k, v), meaning 'subaggregation_name': { dict }
	for k, v := range queryMap {
		// I assume it's new aggregator name
		logger.DebugWithCtx(cw.Ctx).Msgf("Names += %s", k)
		currentAggr.Aggregators = append(currentAggr.Aggregators, model.NewAggregatorEmpty(k))
		cw.parseAggregation(currentAggr, v.(QueryMap), resultAccumulator)
		logger.DebugWithCtx(cw.Ctx).Msgf("Names -= %s", k)
		currentAggr.Aggregators = currentAggr.Aggregators[:len(currentAggr.Aggregators)-1]
	}

	// restore current state, removing subaggregation state
	if filterOnThisLevel {
		currentAggr.whereBuilder = whereBeforeNesting
	}
	if addedNonSchemaField && len(currentAggr.GroupByFields) > 0 {
		currentAggr.GroupByFields = currentAggr.GroupByFields[:len(currentAggr.GroupByFields)-1]
		currentAggr.NonSchemaFields = currentAggr.NonSchemaFields[:len(currentAggr.NonSchemaFields)-1]
	}
	currentAggr.Type = queryTypeBeforeNesting
}

// Tries to parse metrics aggregation from queryMap. If it's not a metrics aggregation, returns false.
func (cw *ClickhouseQueryTranslator) tryMetricsAggregation(queryMap QueryMap) (metricsAggregation, bool) {
	if len(queryMap) != 1 {
		return metricsAggregation{}, false
	}

	// full list: https://www.elastic.co/guide/en/elasticsearch/reference/current/search-Aggregations-metrics.html
	// shouldn't be hard to handle others, if necessary

	metricsAggregations := []string{"sum", "avg", "min", "max", "cardinality", "value_count", "stats"}
	for k, v := range queryMap {
		if slices.Contains(metricsAggregations, k) {
			return metricsAggregation{
				AggrType:   k,
				FieldNames: []string{cw.Table.ResolveField(v.(QueryMap)["field"].(string))},
			}, true
		}
	}

	if percentile, ok := queryMap["percentiles"]; ok {
		fieldName, percentiles := cw.parsePercentilesAggregation(percentile.(QueryMap))
		return metricsAggregation{
			AggrType:    "quantile",
			FieldNames:  []string{fieldName},
			Percentiles: percentiles,
		}, true
	}

	if topMetrics, ok := queryMap["top_metrics"]; ok {
		topMetricsAggrParams := cw.ParseTopMetricsAggregation(topMetrics.(QueryMap))
		return topMetricsAggrParams, true
	}
	if topHits, ok := queryMap["top_hits"]; ok {
		fields := topHits.(QueryMap)["_source"].(QueryMap)["includes"].([]interface{})
		fieldsAsStrings := make([]string, len(fields))
		for i, v := range fields {
			fieldsAsStrings[i] = v.(string)
		}
		return metricsAggregation{
			AggrType:   "top_hits",
			FieldNames: fieldsAsStrings,
		}, true
	}

	// Shortcut here. Percentile_ranks has "field" and a list of "values"
	// I'm keeping all of them in `fieldNames' array for "simplicity".
	if percentileRanks, ok := queryMap["percentile_ranks"]; ok {
		fieldNames := []string{cw.Table.ResolveField(percentileRanks.(QueryMap)["field"].(string))}
		cutValues := percentileRanks.(QueryMap)["values"].([]interface{})
		for _, cutValue := range cutValues {
			switch cutValueTyped := cutValue.(type) {
			case float64:
				fieldNames = append(fieldNames, strconv.FormatFloat(cutValueTyped, 'f', -1, 64))
			case int64:
				fieldNames = append(fieldNames, strconv.FormatInt(cutValueTyped, 10))
			}
		}
		return metricsAggregation{
			AggrType:   "percentile_ranks",
			FieldNames: fieldNames,
		}, true
	}

	return metricsAggregation{}, false
}

// tryBucketAggregation checks if 'queryMap' represents a bucket aggregation on current level and if it does, updates 'currentAggr'.
// Returns:
// * 'success': was it bucket aggreggation?
// * 'nonSchemaFieldAdded': did we add a non-schema field to 'currentAggr', if it turned out to be bucket aggregation? If we did, we need to know, to remove it later.
func (cw *ClickhouseQueryTranslator) tryBucketAggregation(currentAggr *aggrQueryBuilder, queryMap QueryMap) (
	success bool, nonSchemaFieldAdded bool) {
	success, nonSchemaFieldAdded = true, true // returned in most cases
	if histogram, ok := queryMap["histogram"]; ok {
		currentAggr.Type = bucket_aggregations.Histogram{}
		fieldName := strconv.Quote(cw.Table.ResolveField(histogram.(QueryMap)["field"].(string)))
		var interval int
		intervalQueryMap := histogram.(QueryMap)["interval"]
		switch intervalRaw := intervalQueryMap.(type) {
		case string:
			v, err := strconv.Atoi(intervalRaw)
			if err != nil {
				interval = v
			}
		case int:
			interval = intervalRaw
		case float64:
			interval = int(intervalRaw)
		default:
			panic("unexpected type of interval")
		}
		groupByStr := fieldName
		if interval != 1 {
			groupByStr = fmt.Sprintf("floor(%s / %d) * %d AS %s", fieldName, interval, interval, fieldName)
		}
		currentAggr.GroupByFields = append(currentAggr.GroupByFields, groupByStr)
		currentAggr.NonSchemaFields = append(currentAggr.NonSchemaFields, fieldName)
		delete(queryMap, "histogram")
		return
	}
	if dateHistogram, ok := queryMap["date_histogram"]; ok {
		currentAggr.Type = bucket_aggregations.DateHistogram{Interval: cw.extractInterval(dateHistogram.(QueryMap))}
		histogramPartOfQuery := cw.createHistogramPartOfQuery(dateHistogram.(QueryMap))
		currentAggr.GroupByFields = append(currentAggr.GroupByFields, histogramPartOfQuery)
		currentAggr.NonSchemaFields = append(currentAggr.NonSchemaFields, histogramPartOfQuery)
		delete(queryMap, "date_histogram")
		return
	}
	if terms, ok := queryMap["terms"]; ok {
		currentAggr.Type = bucket_aggregations.Terms{}
		fieldName := strconv.Quote(cw.Table.ResolveField(terms.(QueryMap)["field"].(string)))
		currentAggr.GroupByFields = append(currentAggr.GroupByFields, fieldName)
		currentAggr.NonSchemaFields = append(currentAggr.NonSchemaFields, fieldName)
		delete(queryMap, "terms")
		return
	}
	nonSchemaFieldAdded = false
	if _, ok := queryMap["sampler"]; ok {
		currentAggr.Type = metrics_aggregations.Count{}
		delete(queryMap, "sampler")
		return
	}
	// Let's treat random_sampler just like sampler for now, until we add `LIMIT` logic to sampler.
	// Random sampler doesn't have `size` field, but `probability`, so logic in the final version should be different.
	// So far I've only observed its "probability" field to be 1.0, so it's not really important.
	if _, ok := queryMap["random_sampler"]; ok {
		currentAggr.Type = metrics_aggregations.Count{}
		delete(queryMap, "random_sampler")
		return
	}
	if Range, ok := queryMap["range"]; ok {
		currentAggr.whereBuilder = cw.combineWheres(
			currentAggr.whereBuilder,
			cw.parseRange(Range.(QueryMap)),
		)
		delete(queryMap, "range")
		return
	}
	if Bool, ok := queryMap["bool"]; ok {
		currentAggr.whereBuilder = cw.combineWheres(
			currentAggr.whereBuilder,
			cw.parseBool(Bool.(QueryMap)),
		)
		delete(queryMap, "bool")
		return
	}
	success = false
	return
}

func (cw *ClickhouseQueryTranslator) combineWheres(where1, where2 SimpleQuery) SimpleQuery {
	combined := SimpleQuery{
		Sql:      and([]Statement{where1.Sql, where2.Sql}),
		CanParse: where1.CanParse && where2.CanParse,
	}
	if len(where1.FieldName) > 0 && len(where2.FieldName) > 0 {
		logger.WarnWithCtx(cw.Ctx).Msgf("combining 2 where clauses with non-empty field names: %s, %s, where queries: %v %v", where1.FieldName, where2.FieldName, where1, where2)
	}
	if len(where1.FieldName) > 0 {
		combined.FieldName = where1.FieldName
	} else {
		combined.FieldName = where2.FieldName
	}
	return combined
}

// quoteArray returns a new array with the same elements, but quoted
func quoteArray(array []string) []string {
	quotedArray := make([]string, 0, len(array))
	for _, el := range array {
		quotedArray = append(quotedArray, strconv.Quote(el))
	}
	return quotedArray
}
