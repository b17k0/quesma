package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"mitmproxy/quesma/logger"
	"mitmproxy/quesma/model"
	"sort"
	"strings"
	"time"
)

// Implementation of API for Quesma

type FieldInfo = int

const (
	NotExists FieldInfo = iota
	ExistsAndIsBaseType
	ExistsAndIsArray
)

func (lm *LogManager) Query(ctx context.Context, query string) (*sql.Rows, error) {
	rows, err := lm.chDb.QueryContext(ctx, query)
	return rows, err
}

// ProcessSimpleSelectQuery - only WHERE clause
// TODO query param should be type safe Query representing all parts of
// sql statement that were already parsed and not string from which
// we have to extract again different parts like where clause and columns to build a proper result
func (lm *LogManager) ProcessSelectQuery(ctx context.Context, table *Table, query *model.Query) ([]model.QueryResultRow, error) {
	colNames, err := table.extractColumns(query, false)
	rowToScan := make([]interface{}, len(colNames)+len(query.NonSchemaFields))
	if err != nil {
		return nil, err
	}
	rows, err := executeQuery(ctx, lm, table.Name, query.StringFromColumns(colNames), append(colNames, query.NonSchemaFields...), rowToScan)
	if err == nil {
		for _, row := range rows {
			row.Index = table.Name
		}
	}
	return rows, err
}

func makeBuckets(result []model.QueryResultRow, bucket time.Duration) []model.QueryResultRow {
	sort.Slice(result, func(i, j int) bool {
		return result[i].Cols[model.ResultColKeyIndex].Value.(int64) < result[j].Cols[model.ResultColKeyIndex].Value.(int64)
	})
	for i := range result {
		timestamp := result[i].Cols[model.ResultColKeyIndex].Value.(int64) * bucket.Milliseconds()
		result[i].Cols[model.ResultColKeyIndex].Value = timestamp
		result[i].Cols = append(result[i].Cols, model.QueryResultCol{
			ColName: "key_as_string",
			Value:   time.UnixMilli(timestamp).UTC().Format("2006-01-02T15:04:05.000"),
		})
	}
	return result
}

func (lm *LogManager) ProcessHistogramQuery(ctx context.Context, table *Table, query *model.Query, bucket time.Duration) ([]model.QueryResultRow, error) {
	result, err := executeQuery(ctx, lm, table.Name, query.String(), []string{"key", "doc_count"}, []interface{}{int64(0), uint64(0)})
	if err != nil {
		return nil, err
	}
	return makeBuckets(result, bucket), nil
}

// TODO add support for autocomplete for attributes, if we'll find it needed
func (lm *LogManager) ProcessFacetsQuery(ctx context.Context, table *Table, query *model.Query) ([]model.QueryResultRow, error) {
	colNames, err := table.extractColumns(query, false)
	if err != nil {
		return nil, err
	}
	rowToScan := make([]interface{}, len(colNames)+len(query.NonSchemaFields))
	return executeQuery(ctx, lm, table.Name, query.StringFromColumns(colNames), []string{"key", "doc_count"}, rowToScan)
}

var random = rand.New(rand.NewSource(time.Now().UnixNano()))

const slowQueryThreshold = 30 * time.Second
const slowQuerySampleRate = 0.1

func (lm *LogManager) shouldExplainQuery(elapsed time.Duration) bool {
	return elapsed > slowQueryThreshold && random.Float64() < slowQuerySampleRate
}

func (lm *LogManager) explainQuery(ctx context.Context, query string, elapsed time.Duration) {

	explainQuery := "EXPLAIN json=1, indexes=1 " + query

	rows, err := lm.chDb.QueryContext(ctx, explainQuery)
	if err != nil {
		logger.Error().Msgf("failed to explain slow query: %v", err)
	}

	defer rows.Close()
	if rows.Next() {
		var explain string
		err := rows.Scan(&explain)
		if err != nil {
			logger.Error().Msgf("failed to scan slow query explain: %v", err)
			return
		}

		// reformat the explain output to make it one line and more readable
		explain = strings.ReplaceAll(explain, "\n", "")
		explain = strings.ReplaceAll(explain, "  ", "")

		logger.Warn().Msgf("slow query (time: '%s')  query: '%s' -> explain: '%s'", elapsed, query, explain)
	}

	if rows.Err() != nil {
		logger.Error().Msgf("failed to read slow query explain: %v", rows.Err())
	}
}

func executeQuery(ctx context.Context, lm *LogManager, tableName string, queryAsString string, fields []string, rowToScan []interface{}) ([]model.QueryResultRow, error) {
	span := lm.phoneHomeAgent.ClickHouseQueryDuration().Begin()

	rows, err := lm.Query(ctx, queryAsString)
	if err != nil {
		span.End(err)
		return nil, fmt.Errorf("clickhouse: query failed: %v", err)
	}

	res, err := read(tableName, rows, fields, rowToScan)
	elapsed := span.End(nil)
	if err == nil {
		if lm.shouldExplainQuery(elapsed) {
			lm.explainQuery(ctx, queryAsString, elapsed)
		}
	}

	return res, err
}

func (lm *LogManager) ProcessAutocompleteSuggestionsQuery(ctx context.Context, table string, query *model.Query) ([]model.QueryResultRow, error) {
	return executeQuery(ctx, lm, table, query.String(), query.Fields, []interface{}{""})
}

func (lm *LogManager) ProcessTimestampQuery(ctx context.Context, table *Table, query *model.Query) ([]model.QueryResultRow, error) {
	return executeQuery(ctx, lm, table.Name, query.String(), query.Fields, []interface{}{time.Time{}})
}

func (lm *LogManager) ProcessGeneralAggregationQuery(ctx context.Context, table *Table, query *model.Query) ([]model.QueryResultRow, error) {
	colNames, err := table.extractColumns(query, true)
	if err != nil {
		return nil, err
	}
	rowToScan := make([]interface{}, len(colNames))
	return executeQuery(ctx, lm, table.Name, query.String(), colNames, rowToScan)
}

// 'selectFields' are all values that we return from the query, both columns and non-schema fields,
// like e.g. count(), or toInt8(boolField)
func read(tableName string, rows *sql.Rows, selectFields []string, rowToScan []interface{}) ([]model.QueryResultRow, error) {
	rowDb := make([]interface{}, 0, len(rowToScan))
	for i := range rowToScan {
		rowDb = append(rowDb, &rowToScan[i])
	}
	resultRows := make([]model.QueryResultRow, 0)
	for rows.Next() {
		err := rows.Scan(rowDb...)
		if err != nil {
			return nil, fmt.Errorf("clickhouse: scan failed: %v", err)
		}
		resultRow := model.QueryResultRow{Index: tableName, Cols: make([]model.QueryResultCol, len(selectFields))}
		for i, field := range selectFields {
			resultRow.Cols[i] = model.QueryResultCol{ColName: field, Value: rowToScan[i]}
		}
		resultRows = append(resultRows, resultRow)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("clickhouse: iterating over rows failed:  %v", rows.Err())
	}
	err := rows.Close()
	if err != nil {
		return nil, fmt.Errorf("clickhouse: closing rows failed: %v", err)
	}
	return resultRows, nil
}
