package quesma

import (
	"mitmproxy/quesma/model"
	"mitmproxy/quesma/plugins"
)

type TransformationPipeline struct {
	transformers []plugins.QueryTransformer
}

func (o *TransformationPipeline) Transform(queries []*model.Query) ([]*model.Query, error) {
	for _, transformer := range o.transformers {
		queries, _ = transformer.Transform(queries)
	}
	return queries, nil
}
