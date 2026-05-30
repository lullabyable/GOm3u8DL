package m3u8dl

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lullabyable/GOm3u8DL/pkg/model"
)

// Engine is the main entry point for the download engine.
type Engine struct {
	opts   Options
	client *http.Client
}

// New creates a new Engine with the given options.
func New(opts ...Option) *Engine {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return &Engine{
		opts: o,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 16,
			},
		},
	}
}

// GetStreams parses a URL and returns available streams.
// The consumer selects a stream, then calls Download.
func (e *Engine) GetStreams(ctx context.Context, url string, headers map[string]string) ([]model.StreamInfo, error) {
	// TODO: implement HLS/DASH/MSS detection and parsing
	return nil, fmt.Errorf("not implemented")
}

// Download downloads the specified stream.
// Events are delivered via the handler callback.
func (e *Engine) Download(ctx context.Context, req model.DownloadRequest, handler EventHandler) error {
	// TODO: implement download orchestration
	return fmt.Errorf("not implemented")
}

// DownloadWithAutoSelect auto-selects the best stream and downloads it.
func (e *Engine) DownloadWithAutoSelect(ctx context.Context, url string, handler EventHandler) error {
	streams, err := e.GetStreams(ctx, url, nil)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return fmt.Errorf("no streams found")
	}

	// TODO: apply AutoSelectRule, for now pick first stream
	return e.Download(ctx, model.DownloadRequest{
		Stream: &streams[0],
	}, handler)
}
