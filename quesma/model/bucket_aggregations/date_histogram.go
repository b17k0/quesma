package bucket_aggregations

import "mitmproxy/quesma/model"

type QueryTypeDateHistogram struct{}

func (qt QueryTypeDateHistogram) IsBucketAggregation() bool {
	return true
}

func (qt QueryTypeDateHistogram) TranslateSqlResponseToJson(rows []model.QueryResultRow, level int) []model.JsonMap {
	var response []model.JsonMap
	for _, row := range rows {
		response = append(response, model.JsonMap{
			"key":           row.Cols[level].Value,
			"doc_count":     row.Cols[level+1].Value,
			"key_as_string": 1, // TODO fill this
		})
	}
	return response
}

func (qt QueryTypeDateHistogram) String() string {
	return "date_histogram"
}
