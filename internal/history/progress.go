package history

import "context"

type progressKey struct{}

// Progress is a download/backtest status update for the UI.
type Progress struct {
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type ProgressFunc func(Progress)

// WithProgress attaches a reporter to ctx. Safe to call with a nil fn.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// Report publishes a progress event if a reporter is attached.
func Report(ctx context.Context, p Progress) {
	if p.Percent < 0 {
		p.Percent = 0
	}
	if p.Percent > 100 {
		p.Percent = 100
	}
	fn, _ := ctx.Value(progressKey{}).(ProgressFunc)
	if fn != nil {
		fn(p)
	}
}
