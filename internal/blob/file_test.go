package blob

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type scriptedS3 struct {
	head          func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
	put           func(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
	get           func(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
	create        func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error)
	upload        func(*s3.UploadPartInput) (*s3.UploadPartOutput, error)
	complete      func(*s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error)
	abort         func(*s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error)
	createContext func(context.Context, *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error)
	abortContext  func(context.Context, *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error)
}

func (f *scriptedS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return f.head(in)
}
func (f *scriptedS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return f.put(in)
}
func (f *scriptedS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f.get(in)
}

func (f *scriptedS3) CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	if f.createContext != nil {
		return f.createContext(ctx, in)
	}
	return f.create(in)
}
func (f *scriptedS3) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return f.upload(in)
}
func (f *scriptedS3) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return f.complete(in)
}

func (f *scriptedS3) AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	if f.abortContext != nil {
		return f.abortContext(ctx, in)
	}
	return f.abort(in)
}

func notFound() error { return &smithy.GenericAPIError{Code: "NotFound", Message: "missing"} }

func writeUploadFixture(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.parquet")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func multipartTestClient(fake s3API) *Client {
	return &Client{s3: fake, fileUpload: fileUploadConfig{singlePutLimit: 8, multipartPartSize: 3}}
}

func TestPutFileIfChangedSinglePutSendsAndChecksSHA256(t *testing.T) {
	body := []byte("model bytes")
	path := writeUploadFixture(t, body)
	wantRaw := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(wantRaw[:])
	putCalls := 0
	fake := &scriptedS3{
		head: func(in *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			if in.ChecksumMode != types.ChecksumModeEnabled {
				t.Fatal("checksum mode not enabled")
			}
			return nil, notFound()
		},
		put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			putCalls++
			got, err := io.ReadAll(in.Body)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("body = %q, %v", got, err)
			}
			if in.ChecksumAlgorithm != types.ChecksumAlgorithmSha256 || aws.ToString(in.ChecksumSHA256) != want || aws.ToInt64(in.ContentLength) != int64(len(body)) {
				t.Fatalf("single put checksum/length = %#v", in)
			}
			if in.Metadata["sha256"] != hex.EncodeToString(wantRaw[:]) {
				t.Fatalf("metadata = %v", in.Metadata)
			}
			return &s3.PutObjectOutput{ChecksumSHA256: &want, ChecksumType: types.ChecksumTypeFullObject}, nil
		},
	}
	uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "bucket", "key", path, "application/octet-stream")
	if err != nil || !uploaded || putCalls != 1 {
		t.Fatalf("PutFileIfChanged = %t, %v; calls=%d", uploaded, err, putCalls)
	}
}

func TestPutFileIfChangedUsesChecksumHeadAndLegacyETag(t *testing.T) {
	body := []byte("same")
	path := writeUploadFixture(t, body)
	sha := sha256.Sum256(body)
	md5sum := md5.Sum(body)
	cases := []struct {
		name string
		head *s3.HeadObjectOutput
	}{
		{name: "full checksum", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(body))), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sha[:])), ChecksumType: types.ChecksumTypeFullObject}},
		{name: "legacy etag", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(body))), ETag: aws.String(`"` + hex.EncodeToString(md5sum[:]) + `"`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &scriptedS3{head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return tc.head, nil }, put: func(*s3.PutObjectInput) (*s3.PutObjectOutput, error) { t.Fatal("unexpected put"); return nil, nil }}
			uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "bucket", "key", path, "x")
			if err != nil || uploaded {
				t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
			}
		})
	}
}

func TestHeadMatchesRequiresAuthoritativeChecksumTypeOrLegacySingleETag(t *testing.T) {
	body := []byte("same")
	sha := sha256.Sum256(body)
	md5sum := md5.Sum(body)
	digest := fileDigest{sha256Raw: sha, md5Raw: md5sum, size: int64(len(body))}
	checksum := digest.sha256Base64()
	legacy := `"` + digest.md5Hex() + `"`
	for _, tc := range []struct {
		name string
		head *s3.HeadObjectOutput
		want bool
	}{
		{name: "authoritative", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, want: true},
		{name: "authoritative without size", head: &s3.HeadObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, want: true},
		{name: "authoritative ignores diagnostic size", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size + 1), ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, want: true},
		{name: "wrong type", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeComposite}},
		{name: "legacy", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), ETag: &legacy}, want: true},
		{name: "legacy ignores diagnostic size", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size + 1), ETag: &legacy}, want: true},
		{name: "multipart etag is not legacy", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), ETag: aws.String(legacy + "-2")}},
		{name: "metadata cannot substitute", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), Metadata: map[string]string{"sha256": digest.sha256Hex()}}},
		{name: "size cannot substitute", head: &s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size)}},
		{name: "crc32 disables legacy", head: &s3.HeadObjectOutput{ChecksumCRC32: aws.String("crc"), ETag: &legacy}},
		{name: "crc32c disables legacy", head: &s3.HeadObjectOutput{ChecksumCRC32C: aws.String("crc"), ETag: &legacy}},
		{name: "crc64 disables legacy", head: &s3.HeadObjectOutput{ChecksumCRC64NVME: aws.String("crc"), ETag: &legacy}},
		{name: "sha1 disables legacy", head: &s3.HeadObjectOutput{ChecksumSHA1: aws.String("sha"), ETag: &legacy}},
		{name: "md5 checksum disables legacy", head: &s3.HeadObjectOutput{ChecksumMD5: aws.String("md5"), ETag: &legacy}},
		{name: "sha512 disables legacy", head: &s3.HeadObjectOutput{ChecksumSHA512: aws.String("sha"), ETag: &legacy}},
		{name: "xxhash128 disables legacy", head: &s3.HeadObjectOutput{ChecksumXXHASH128: aws.String("xx"), ETag: &legacy}},
		{name: "xxhash3 disables legacy", head: &s3.HeadObjectOutput{ChecksumXXHASH3: aws.String("xx"), ETag: &legacy}},
		{name: "xxhash64 disables legacy", head: &s3.HeadObjectOutput{ChecksumXXHASH64: aws.String("xx"), ETag: &legacy}},
		{name: "checksum type disables legacy", head: &s3.HeadObjectOutput{ChecksumType: types.ChecksumTypeFullObject, ETag: &legacy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := headMatches(tc.head, digest); got != tc.want {
				t.Fatalf("headMatches = %t, want %t", got, tc.want)
			}
		})
	}

	partA, partB := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	digest.parts = [][sha256.Size]byte{partA, partB}
	composite := digest.authoritativeChecksum()
	if !headMatches(&s3.HeadObjectOutput{ContentLength: aws.Int64(digest.size), ChecksumSHA256: &composite, ChecksumType: types.ChecksumTypeComposite}, digest) {
		t.Fatal("matching composite checksum was not authoritative")
	}
}

func TestPutFileIfChangedDoesNotTrustWrongStoredChecksum(t *testing.T) {
	path := writeUploadFixture(t, []byte("same"))
	putCalled := false
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ContentLength: aws.Int64(4), ChecksumSHA256: aws.String("wrong"), ChecksumType: types.ChecksumTypeFullObject, ETag: aws.String(`"51037a4a37730f52c8732586d3aaa316"`)}, nil
		},
		put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			putCalled = true
			return &s3.PutObjectOutput{ChecksumSHA256: in.ChecksumSHA256, ChecksumType: types.ChecksumTypeFullObject}, nil
		},
	}
	if uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", path, "x"); err != nil || !uploaded || !putCalled {
		t.Fatalf("PutFileIfChanged = %t, %v; put=%t", uploaded, err, putCalled)
	}
}

func TestPutFileIfChangedRejectsSymlinkAndReturnedChecksumMismatch(t *testing.T) {
	target := writeUploadFixture(t, []byte("same"))
	link := filepath.Join(t.TempDir(), "artifact.parquet")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Client{}).PutFileIfChanged(context.Background(), "b", "k", link, "x"); err == nil {
		t.Fatal("accepted symlink")
	}

	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		put: func(*s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			return &s3.PutObjectOutput{ChecksumSHA256: aws.String("wrong"), ChecksumType: types.ChecksumTypeFullObject}, nil
		},
	}
	if uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", target, "x"); err == nil || uploaded {
		t.Fatalf("mismatched response = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedMultipartCompositeAndParts(t *testing.T) {
	body := []byte("0123456789")
	path := writeUploadFixture(t, body)
	uploadID := "upload"
	partCount := 0
	compositeHash := sha256.New()
	for _, part := range [][]byte{body[0:3], body[3:6], body[6:9], body[9:10]} {
		sum := sha256.Sum256(part)
		_, _ = compositeHash.Write(sum[:])
	}
	wantComposite := base64.StdEncoding.EncodeToString(compositeHash.Sum(nil)) + "-4"
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		create: func(in *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			if in.ChecksumAlgorithm != types.ChecksumAlgorithmSha256 || in.ChecksumType != types.ChecksumTypeComposite {
				t.Fatalf("create = %#v", in)
			}
			return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
		},
		upload: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
			partCount++
			body, err := io.ReadAll(in.Body)
			if err != nil || int64(len(body)) != aws.ToInt64(in.ContentLength) {
				t.Fatalf("part body = %d, %v", len(body), err)
			}
			digest := sha256.Sum256(body)
			want := base64.StdEncoding.EncodeToString(digest[:])
			if aws.ToString(in.ChecksumSHA256) != want {
				t.Fatalf("part checksum = %q, want %q", aws.ToString(in.ChecksumSHA256), want)
			}
			etag := "etag"
			return &s3.UploadPartOutput{ETag: &etag, ChecksumSHA256: &want}, nil
		},
		complete: func(in *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			if len(in.MultipartUpload.Parts) != partCount || in.ChecksumType != types.ChecksumTypeComposite || aws.ToString(in.ChecksumSHA256) != wantComposite {
				t.Fatalf("complete = %#v", in)
			}
			return &s3.CompleteMultipartUploadOutput{ChecksumSHA256: &wantComposite, ChecksumType: types.ChecksumTypeComposite}, nil
		},
		abort: func(*s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
			t.Fatal("unexpected abort")
			return nil, nil
		},
	}
	uploaded, err := multipartTestClient(fake).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err != nil || !uploaded || partCount != 4 {
		t.Fatalf("multipart = %t, %v; parts=%d", uploaded, err, partCount)
	}
}

func TestPutFileIfChangedMultipartFailureAbortsAndJoins(t *testing.T) {
	path := writeUploadFixture(t, []byte("0123456789"))
	uploadID := "upload"
	uploadErr, abortErr := errors.New("part failed"), errors.New("abort failed")
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		create: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
		},
		upload: func(*s3.UploadPartInput) (*s3.UploadPartOutput, error) { return nil, uploadErr },
		abort:  func(*s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) { return nil, abortErr },
	}
	_, err := multipartTestClient(fake).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if !errors.Is(err, uploadErr) || !errors.Is(err, abortErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestPutFileIfChangedMultipartCancellationAfterCreateAbortsWithLiveContext(t *testing.T) {
	path := writeUploadFixture(t, []byte("0123456789"))
	ctx, cancel := context.WithCancel(context.Background())
	uploadID := "upload"
	abortCalls := 0
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		createContext: func(context.Context, *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			cancel()
			return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
		},
		upload: func(*s3.UploadPartInput) (*s3.UploadPartOutput, error) { return nil, ctx.Err() },
		abortContext: func(abortCtx context.Context, _ *s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
			abortCalls++
			if err := abortCtx.Err(); err != nil {
				t.Fatalf("abort context = %v", err)
			}
			return &s3.AbortMultipartUploadOutput{}, nil
		},
	}
	uploaded, err := multipartTestClient(fake).PutFileIfChanged(ctx, "b", "k", path, "x")
	if uploaded || !errors.Is(err, context.Canceled) || abortCalls != 1 {
		t.Fatalf("PutFileIfChanged = %t, %v; aborts=%d", uploaded, err, abortCalls)
	}
}

func TestPutFileIfChangedCompleteFailuresNeverSucceed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		completeErr error
		mismatch    bool
		wantAbort   bool
	}{
		{name: "ambiguous transport", completeErr: errors.New("connection reset"), wantAbort: true},
		{name: "successful response mismatch", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadFixture(t, []byte("0123456789"))
			uploadID := "upload"
			abortCalls := 0
			fake := &scriptedS3{
				head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
				create: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
					return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
				},
				upload: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
					etag := "etag"
					return &s3.UploadPartOutput{ETag: &etag, ChecksumSHA256: in.ChecksumSHA256}, nil
				},
				complete: func(in *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
					if tc.completeErr != nil {
						return nil, tc.completeErr
					}
					checksum := aws.ToString(in.ChecksumSHA256)
					if tc.mismatch {
						checksum = "wrong"
					}
					return &s3.CompleteMultipartUploadOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeComposite}, nil
				},
				abort: func(*s3.AbortMultipartUploadInput) (*s3.AbortMultipartUploadOutput, error) {
					abortCalls++
					return &s3.AbortMultipartUploadOutput{}, nil
				},
			}
			uploaded, err := multipartTestClient(fake).PutFileIfChanged(context.Background(), "b", "k", path, "x")
			wantAborts := 0
			if tc.wantAbort {
				wantAborts = 1
			}
			if err == nil || uploaded || abortCalls != wantAborts {
				t.Fatalf("PutFileIfChanged = %t, %v; aborts=%d", uploaded, err, abortCalls)
			}
		})
	}
}

func TestPutFileIfChangedRetryAcceptsCommittedMultipartAfterResponseMismatch(t *testing.T) {
	path := writeUploadFixture(t, []byte("0123456789"))
	uploadID := "upload"
	createCalls := 0
	var committedChecksum string
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			if committedChecksum == "" {
				return nil, notFound()
			}
			return &s3.HeadObjectOutput{ChecksumSHA256: &committedChecksum, ChecksumType: types.ChecksumTypeComposite}, nil
		},
		create: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			createCalls++
			return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
		},
		upload: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
			etag := "etag"
			return &s3.UploadPartOutput{ETag: &etag, ChecksumSHA256: in.ChecksumSHA256}, nil
		},
		complete: func(in *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			committedChecksum = aws.ToString(in.ChecksumSHA256)
			return &s3.CompleteMultipartUploadOutput{ChecksumSHA256: aws.String("wrong"), ChecksumType: types.ChecksumTypeComposite}, nil
		},
	}
	client := multipartTestClient(fake)
	if uploaded, err := client.PutFileIfChanged(context.Background(), "b", "k", path, "x"); err == nil || uploaded {
		t.Fatalf("first PutFileIfChanged = %t, %v", uploaded, err)
	}
	if uploaded, err := client.PutFileIfChanged(context.Background(), "b", "k", path, "x"); err != nil || uploaded || createCalls != 1 {
		t.Fatalf("retry PutFileIfChanged = %t, %v; creates=%d", uploaded, err, createCalls)
	}
}

func TestPutReaderIfChangedRehashesTheSameBoundedReader(t *testing.T) {
	body := []byte("same")
	original := append([]byte(nil), body...)
	sha := sha256.Sum256(original)
	checksum := base64.StdEncoding.EncodeToString(sha[:])
	fake := &scriptedS3{head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		copy(body, []byte("else"))
		return &s3.HeadObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, nil
	}}
	uploaded, err := (&Client{s3: fake}).PutReaderIfChanged(context.Background(), "b", "k", bytes.NewReader(body), int64(len(body)), "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "reader changed during publication") {
		t.Fatalf("PutReaderIfChanged = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedFinalIdentityGateRejectsUnchangedPathSymlinkReplacement(t *testing.T) {
	body := []byte("same")
	path := writeUploadFixture(t, body)
	target := writeUploadFixture(t, body)
	sha := sha256.Sum256(body)
	checksum := base64.StdEncoding.EncodeToString(sha[:])
	fake := &scriptedS3{head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return &s3.HeadObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, nil
	}}
	uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "changed during publication") {
		t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedFinalIdentityGateRejectsUnchangedInPlaceMutation(t *testing.T) {
	body := []byte("same")
	path := writeUploadFixture(t, body)
	sha := sha256.Sum256(body)
	checksum := base64.StdEncoding.EncodeToString(sha[:])
	fake := &scriptedS3{head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte("else"), 0); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return &s3.HeadObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, nil
	}}
	uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "reader changed during publication") {
		t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedFinalIdentityGateRejectsUploadPathSymlinkReplacement(t *testing.T) {
	body := []byte("same")
	path := writeUploadFixture(t, body)
	sha := sha256.Sum256(body)
	checksum := base64.StdEncoding.EncodeToString(sha[:])
	target := writeUploadFixture(t, body)
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			if _, err := io.ReadAll(in.Body); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return &s3.PutObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, nil
		},
	}
	uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "changed during publication") {
		t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedFinalIdentityGateRejectsUploadInPlaceMutation(t *testing.T) {
	body := []byte("same")
	path := writeUploadFixture(t, body)
	sha := sha256.Sum256(body)
	checksum := base64.StdEncoding.EncodeToString(sha[:])
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		put: func(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			if _, err := io.ReadAll(in.Body); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte("else"), 0); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return &s3.PutObjectOutput{ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeFullObject}, nil
		},
	}
	uploaded, err := (&Client{s3: fake}).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "changed during publication") {
		t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
	}
}

func TestPutFileIfChangedFinalIdentityGateRejectsMultipartMutation(t *testing.T) {
	path := writeUploadFixture(t, []byte("0123456789"))
	uploadID := "upload"
	fake := &scriptedS3{
		head: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) { return nil, notFound() },
		create: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: &uploadID}, nil
		},
		upload: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
			if _, err := io.ReadAll(in.Body); err != nil {
				t.Fatal(err)
			}
			etag := "etag"
			return &s3.UploadPartOutput{ETag: &etag, ChecksumSHA256: in.ChecksumSHA256}, nil
		},
		complete: func(in *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			file, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte("mutation!"), 0); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return &s3.CompleteMultipartUploadOutput{ChecksumSHA256: in.ChecksumSHA256, ChecksumType: types.ChecksumTypeComposite}, nil
		},
	}
	uploaded, err := multipartTestClient(fake).PutFileIfChanged(context.Background(), "b", "k", path, "x")
	if err == nil || uploaded || !strings.Contains(err.Error(), "reader changed during publication") {
		t.Fatalf("PutFileIfChanged = %t, %v", uploaded, err)
	}
}
