package llm

import "context"

// defaultModelProvider fills in the model from the configuration when a request
// does not name one.
//
// This lives here rather than in each wire implementation because the model is a
// property of the configuration, not of the call: the agent builds a neutral
// Request and the provider knows what it was configured with. Getting this wrong
// is invisible in tests (the offline provider ignores the model) and fatal in
// production, where the API rejects an empty "model" field.
type defaultModelProvider struct {
	Provider
	model string
}

// WithDefaultModel returns a provider that substitutes model for any request that
// does not carry one.
func WithDefaultModel(p Provider, model string) Provider {
	if p == nil || model == "" {
		return p
	}
	if _, ok := p.(defaultModelProvider); ok {
		return p
	}
	return defaultModelProvider{Provider: p, model: model}
}

// Stream fills in the model and delegates.
func (d defaultModelProvider) Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error {
	if req.Model == "" {
		req.Model = d.model
	}
	return d.Provider.Stream(ctx, req, onEvent)
}

// Complete fills in the model and delegates.
func (d defaultModelProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if req.Model == "" {
		req.Model = d.model
	}
	return d.Provider.Complete(ctx, req)
}
