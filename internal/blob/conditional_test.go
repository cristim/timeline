package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func TestPutIfAbsentUsesConditionalChecksumAndClassifiesConflict(t *testing.T) {
	body := []byte("pointer")
	fake := &scriptedS3{put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		if aws.ToString(in.IfNoneMatch) != "*" || in.ChecksumAlgorithm != types.ChecksumAlgorithmSha256 || in.ChecksumSHA256 == nil {
			t.Fatalf("put = %#v", in)
		}
		return &s3.PutObjectOutput{ChecksumSHA256: in.ChecksumSHA256, ChecksumType: types.ChecksumTypeFullObject}, nil
	}}
	created, err := (&Client{s3: fake}).PutIfAbsent(context.Background(), "b", "k", body, "application/json")
	if err != nil || !created {
		t.Fatalf("PutIfAbsent = %t, %v", created, err)
	}

	fake.put = func(*s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "won"}
	}
	created, err = (&Client{s3: fake}).PutIfAbsent(context.Background(), "b", "k", body, "application/json")
	if created || !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("PutIfAbsent conflict = %t, %v", created, err)
	}
}

func TestPutIfAbsentRetriesConditionalRequestConflict(t *testing.T) {
	conflict := &smithy.GenericAPIError{Code: "ConditionalRequestConflict", Message: "race"}
	conflict409 := &smithy.GenericAPIError{Code: "409", Message: "race"}
	precondition := &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "won"}
	other := errors.New("transport")
	for _, tc := range []struct {
		name        string
		responses   []error
		wantCreated bool
		wantError   error
		wantCalls   int
	}{
		{name: "conflict then success", responses: []error{conflict, nil}, wantCreated: true, wantCalls: 2},
		{name: "conflict then precondition", responses: []error{conflict, precondition}, wantError: ErrPreconditionFailed, wantCalls: 2},
		{name: "conflict exhaustion", responses: []error{conflict409, conflict409, conflict409}, wantError: conflict409, wantCalls: 3},
		{name: "other errors are not retried", responses: []error{other}, wantError: other, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fake := &scriptedS3{put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
				response := tc.responses[calls]
				calls++
				if response != nil {
					return nil, response
				}
				return &s3.PutObjectOutput{ChecksumSHA256: in.ChecksumSHA256, ChecksumType: types.ChecksumTypeFullObject}, nil
			}}
			created, err := (&Client{s3: fake}).PutIfAbsent(context.Background(), "b", "k", []byte("body"), "x")
			if created != tc.wantCreated || calls != tc.wantCalls || (tc.wantError != nil && !errors.Is(err, tc.wantError)) || (tc.wantError == nil && err != nil) {
				t.Fatalf("PutIfAbsent = %t, %v; calls=%d", created, err, calls)
			}
		})
	}
}

func TestPutIfAbsentStopsConflictRetryWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fake := &scriptedS3{put: func(*s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		calls++
		cancel()
		return nil, &smithy.GenericAPIError{Code: "ConditionalRequestConflict", Message: "race"}
	}}
	created, err := (&Client{s3: fake}).PutIfAbsent(ctx, "b", "k", []byte("body"), "x")
	if created || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("PutIfAbsent = %t, %v; calls=%d", created, err, calls)
	}
}

type closeErrorBody struct {
	io.Reader
	err error
}

func (b closeErrorBody) Close() error { return b.err }

func TestGetBoundedChecksHeaderStreamLimitAndClose(t *testing.T) {
	closeErr := errors.New("close failed")
	fake := &scriptedS3{get: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{ContentLength: aws.Int64(3), Body: closeErrorBody{Reader: strings.NewReader("abc"), err: closeErr}}, nil
	}}
	body, err := (&Client{s3: fake}).GetBounded(context.Background(), "b", "k", 3)
	if string(body) != "abc" || !errors.Is(err, closeErr) {
		t.Fatalf("GetBounded = %q, %v", body, err)
	}

	fake.get = func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("abcd"))}, nil
	}
	if _, err := (&Client{s3: fake}).GetBounded(context.Background(), "b", "k", 3); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("stream limit error = %v", err)
	}

	fake.get = func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{ContentLength: aws.Int64(4), Body: io.NopCloser(strings.NewReader("abcd"))}, nil
	}
	if _, err := (&Client{s3: fake}).GetBounded(context.Background(), "b", "k", 3); err == nil || !strings.Contains(err.Error(), "object size") {
		t.Fatalf("header limit error = %v", err)
	}
}

func TestGetBoundedHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &scriptedS3{get: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("abc"))}, nil
	}}
	if _, err := (&Client{s3: fake}).GetBounded(ctx, "b", "k", 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetBounded error = %v", err)
	}
}
