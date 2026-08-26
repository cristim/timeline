// Package blob is the single S3 code path (DEV-1): MinIO in dev, real S3 in
// cloud, switched purely by S3_ENDPOINT / S3_FORCE_PATH_STYLE env.
package blob

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type Client struct {
	s3 *s3.Client
}

func New(ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	endpoint := os.Getenv("S3_ENDPOINT")
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = os.Getenv("S3_FORCE_PATH_STYLE") == "true"
	})
	return &Client{s3: cli}, nil
}

// PutIfChanged uploads body unless an object with identical content already
// exists (ETag == md5, valid for the non-multipart puts we do). This is what
// makes re-bakes incremental (ARCH-4). Returns whether an upload happened.
func (c *Client) PutIfChanged(ctx context.Context, bucket, key string, body []byte, contentType string) (bool, error) {
	sum := md5.Sum(body)
	local := hex.EncodeToString(sum[:])
	head, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err == nil && strings.Trim(aws.ToString(head.ETag), `"`) == local {
		return false, nil
	}
	if err != nil && !isNotFound(err) {
		return false, fmt.Errorf("head s3://%s/%s: %w", bucket, key, err)
	}
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &bucket,
		Key:         &key,
		Body:        bytes.NewReader(body),
		ContentType: &contentType,
	})
	if err != nil {
		return false, fmt.Errorf("put s3://%s/%s: %w", bucket, key, err)
	}
	return true, nil
}

func (c *Client) PutJSON(ctx context.Context, bucket, key string, v any) (bool, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", key, err)
	}
	return c.PutIfChanged(ctx, bucket, key, body, "application/json")
}

func (c *Client) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("get s3://%s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(out.Body); err != nil {
		return nil, fmt.Errorf("read s3://%s/%s: %w", bucket, key, err)
	}
	return buf.Bytes(), nil
}

func isNotFound(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		code := ae.ErrorCode()
		return code == "NotFound" || code == "NoSuchKey" || code == "404"
	}
	return false
}
