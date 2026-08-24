package blob

import "context"

// BucketSink adapts Client to the bake.Sink interface for one bucket.
type BucketSink struct {
	Client *Client
	Bucket string
}

func (s BucketSink) Put(ctx context.Context, key string, body []byte, contentType string) (bool, error) {
	return s.Client.PutIfChanged(ctx, s.Bucket, key, body, contentType)
}
