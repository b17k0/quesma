package model

import (
	"mitmproxy/quesma/logger"
	"strconv"
	"strings"
)

const RowNumberColumnName = "row_number"
const EmptyFieldSelection = "''" // we can query SELECT '', that's why such quotes

// implements String() (now) and MakeResponse() interface (in the future (?))
type Query struct {
	Fields          []string // Fields in 'SELECT Fields FROM ...'
	NonSchemaFields []string // Fields that are not in schema, but are in 'SELECT ...', e.g. count()
	WhereClause     string   // "WHERE ..." until next clause like GROUP BY/ORDER BY, etc.
	GroupByFields   []string // if not empty, we do GROUP BY GroupByFields... They are quoted if they are column names, unquoted if non-schema. So no quotes need to be added.
	SuffixClauses   []string // ORDER BY, etc.
	FromClause      string   // usually just "tableName", or databaseName."tableName". Sometimes a subquery e.g. (SELECT ...)
	CanParse        bool     // true <=> query is valid
}

// implements String() (now) and MakeResponse() interface (in the future (?))
type QueryWithAggregation struct {
	Query
	AggregatorsNames []string // keeps names of aggregators, e.g. "0", "1", "2", "suggestions". Needed for JSON response.
	Type             QueryType
}

// returns string with * in SELECT
func (q *Query) String() string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	for i, field := range q.Fields {
		if field == "*" || field == EmptyFieldSelection {
			sb.WriteString(field)
		} else {
			sb.WriteString(strconv.Quote(field))
		}
		if i < len(q.Fields)-1 || len(q.NonSchemaFields) > 0 {
			sb.WriteString(", ")
		}
	}
	for i, field := range q.NonSchemaFields {
		sb.WriteString(field)
		if i < len(q.NonSchemaFields)-1 {
			sb.WriteString(", ")
		}
	}
	where := " WHERE "
	if len(q.WhereClause) == 0 {
		where = ""
	}
	sb.WriteString(" FROM " + q.FromClause + where + q.WhereClause + " " + strings.Join(q.SuffixClauses, " "))
	if len(q.GroupByFields) > 0 {
		sb.WriteString(" GROUP BY (")
		for i, field := range q.GroupByFields {
			sb.WriteString(field)
			if i < len(q.GroupByFields)-1 {
				sb.WriteString(", ")
			}
		}
		sb.WriteString(")")
	}
	return sb.String()
}

// returns string without * in SELECT
// colNames - list of columns (schema fields) for SELECT
func (q *Query) StringFromColumns(colNames []string) string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	for i, field := range colNames {
		if field != EmptyFieldSelection {
			sb.WriteString(strconv.Quote(field))
		} else {
			sb.WriteString(field)
		}
		if i < len(colNames)-1 || len(q.NonSchemaFields) > 0 {
			sb.WriteString(", ")
		}
	}
	for i, field := range q.NonSchemaFields {
		sb.WriteString(field)
		if i < len(q.NonSchemaFields)-1 {
			sb.WriteString(", ")
		}
	}
	where := " WHERE "
	if len(q.WhereClause) == 0 {
		where = ""
	}
	sb.WriteString(" FROM " + q.FromClause + where + q.WhereClause + " " + strings.Join(q.SuffixClauses, " "))
	return sb.String()
}

func (q *Query) IsWildcard() bool {
	return len(q.Fields) == 1 && q.Fields[0] == "*"
}

// CopyAggregationFields copies all aggregation fields from qwa to q
func (q *QueryWithAggregation) CopyAggregationFields(qwa QueryWithAggregation) {
	q.GroupByFields = make([]string, len(qwa.GroupByFields))
	copy(q.GroupByFields, qwa.GroupByFields)

	q.Fields = make([]string, len(qwa.Fields))
	copy(q.Fields, qwa.Fields)

	q.NonSchemaFields = make([]string, len(qwa.NonSchemaFields))
	copy(q.NonSchemaFields, qwa.NonSchemaFields)

	q.AggregatorsNames = make([]string, len(qwa.AggregatorsNames))
	copy(q.AggregatorsNames, qwa.AggregatorsNames)

	// let's leave this comment until algorithm is 100% correct. I'm still trying to figure out what's the correct way to do it.
	// also probably move this to some other function
	/* if q.Type.IsBucketAggregation() && len(q.Fields) > 0 {
		q.Fields = q.Fields[:len(q.Fields)-1]
	} */
}

// RemoveEmptyGroupBy removes EmptyFieldSelection from GroupByFields
func (q *QueryWithAggregation) RemoveEmptyGroupBy() {
	nonEmptyFields := make([]string, 0)
	for _, field := range q.GroupByFields {
		if field != EmptyFieldSelection {
			nonEmptyFields = append(nonEmptyFields, field)
		}
	}
	q.GroupByFields = nonEmptyFields
}

// TrimKeywordFromFields trims .keyword from fields and group by fields
// In future probably handle it in a better way
func (q *QueryWithAggregation) TrimKeywordFromFields() {
	for i := range q.Fields {
		if strings.HasSuffix(q.Fields[i], ".keyword") {
			logger.Warn().Msgf("Trimming .keyword from field %s", q.Fields[i])
		}
		q.Fields[i] = strings.TrimSuffix(q.Fields[i], ".keyword")
	}
	for i := range q.GroupByFields {
		if strings.HasSuffix(q.GroupByFields[i], ".keyword") {
			logger.Warn().Msgf("Trimming .keyword from group by field %s", q.GroupByFields[i])
		}
		q.GroupByFields[i] = strings.TrimSuffix(q.GroupByFields[i], ".keyword")
	}
}

type AsyncSearchQueryType int
type SearchQueryType int

const (
	Histogram AsyncSearchQueryType = iota
	AggsByField
	ListByField
	ListAllFields
	EarliestLatestTimestamp // query for 2 timestamps: earliest and latest
	CountAsync
	None // called None, not Normal, like below, as it basically never happens, I don't even know how to trigger it/reply to this
)

const (
	Count SearchQueryType = iota
	Normal
)

func (queryType AsyncSearchQueryType) String() string {
	return []string{"Histogram", "AggsByField", "ListByField", "ListAllFields", "EarliestLatestTimestamp", "CountAsync", "None"}[queryType]
}

func (queryType SearchQueryType) String() string {
	return []string{"Count", "Normal"}[queryType]
}

type QueryInfoAsyncSearch struct {
	Typ       AsyncSearchQueryType
	FieldName string
	Interval  string
	I1        int
	I2        int
}

func NewQueryInfoAsyncSearchNone() QueryInfoAsyncSearch {
	return QueryInfoAsyncSearch{Typ: None}
}
