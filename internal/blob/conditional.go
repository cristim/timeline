package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrPreconditionFailed reports that a conditional immutable create lost to
// an existing object.
var ErrPreconditionFailed = errors.New("object already exists")

const conditionalPutConflictRetries = 2

// PutIfAbsent atomically creates an object and reports a distinct precondition
// error when another writer already created the key.
func (c *Client) PutIfAbsent(ctx context.Context, bucket, key string, body []byte, contentType string) (bool, error) {
	star := "*"
	digest := sha256.Sum256(body)
	checksum := base64.StdEncoding.EncodeToString(digest[:])
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("create s3://%s/%s: %w", bucket, key, err)
		}
		out, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &bucket, Key: &key, Body: bytes.NewReader(body), ContentType: &contentType,
			ContentLength: int64Pointer(int64(len(body))), IfNoneMatch: &star,
			ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: &checksum,
		})
		if err == nil {
			if out == nil || out.ChecksumSHA256 == nil || *out.ChecksumSHA256 != checksum || out.ChecksumType != types.ChecksumTypeFullObject {
				return false, fmt.Errorf("create s3://%s/%s returned a mismatched SHA-256 checksum or type", bucket, key)
			}
			return true, nil
		}
		if isPreconditionFailed(err) {
			return false, fmt.Errorf("create s3://%s/%s: %w", bucket, key, ErrPreconditionFailed)
		}
		if !isConditionalRequestConflict(err) || attempt == conditionalPutConflictRetries {
			return false, fmt.Errorf("create s3://%s/%s: %w", bucket, key, err)
		}
	}
}

func (c *Client) GetBounded(ctx context.Context, bucket, key string, limit int64) (_ []byte, err error) {
	if limit < 0 || limit == math.MaxInt64 {
		return nil, fmt.Errorf("get s3://%s/%s: invalid limit %d", bucket, key, limit)
	}
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("get s3://%s/%s: %w", bucket, key, err)
	}
	if out == nil || out.Body == nil {
		return nil, fmt.Errorf("get s3://%s/%s: response has no body", bucket, key)
	}
	defer func() { err = errors.Join(err, out.Body.Close()) }()
	if out.ContentLength != nil && *out.ContentLength > limit {
		return nil, fmt.Errorf("get s3://%s/%s: object size %d exceeds limit %d", bucket, key, *out.ContentLength, limit)
	}
	body, err := readBounded(ctx, out.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("read s3://%s/%s: %w", bucket, key, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("get s3://%s/%s: object exceeds limit %d", bucket, key, limit)
	}
	return body, nil
}

func readBounded(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	buffer := make([]byte, 32*1024)
	var body []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := limited.Read(buffer)
		if n > 0 {
			body = append(body, buffer[:n]...)
		}
		if errors.Is(err, io.EOF) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return body, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func isPreconditionFailed(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "PreconditionFailed" || apiError.ErrorCode() == "412"
}

func isConditionalRequestConflict(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "ConditionalRequestConflict" || apiError.ErrorCode() == "409"
}

func int64Pointer(value int64) *int64 { return &value }
