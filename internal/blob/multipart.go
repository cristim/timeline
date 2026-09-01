package blob

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const multipartAbortTimeout = 30 * time.Second

func (c *Client) putMultipartFile(ctx context.Context, bucket, key string, reader io.ReaderAt, contentType string, digest fileDigest) error {
	created, err := c.s3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: &bucket, Key: &key, ContentType: &contentType,
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumType: types.ChecksumTypeComposite,
		Metadata: map[string]string{"sha256": digest.sha256Hex(), "size": strconv.FormatInt(digest.size, 10)},
	})
	if err != nil {
		return fmt.Errorf("create multipart s3://%s/%s: %w", bucket, key, err)
	}
	if created == nil {
		return fmt.Errorf("create multipart s3://%s/%s returned no output", bucket, key)
	}
	uploadID := aws.ToString(created.UploadId)
	if uploadID == "" {
		return fmt.Errorf("create multipart s3://%s/%s returned no upload id", bucket, key)
	}

	abort := func(primary error) error {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), multipartAbortTimeout)
		defer cancel()
		_, abortErr := c.s3.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{
			Bucket: &bucket, Key: &key, UploadId: &uploadID,
		})
		if abortErr != nil {
			abortErr = fmt.Errorf("abort multipart s3://%s/%s: %w", bucket, key, abortErr)
		}
		return errors.Join(primary, abortErr)
	}

	parts := make([]types.CompletedPart, 0, len(digest.parts))
	for index, raw := range digest.parts {
		partNumber := int32(index + 1)
		offset := int64(index) * digest.partSize
		length := digest.partSize
		if remaining := digest.size - offset; remaining < length {
			length = remaining
		}
		checksum := base64.StdEncoding.EncodeToString(raw[:])
		out, uploadErr := c.s3.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: &bucket, Key: &key, UploadId: &uploadID, PartNumber: &partNumber,
			Body: io.NewSectionReader(reader, offset, length), ContentLength: &length,
			ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: &checksum,
		})
		if uploadErr != nil {
			return abort(fmt.Errorf("upload multipart part %d to s3://%s/%s: %w", partNumber, bucket, key, uploadErr))
		}
		if out == nil {
			return abort(fmt.Errorf("upload multipart part %d to s3://%s/%s returned no output", partNumber, bucket, key))
		}
		etag := aws.ToString(out.ETag)
		if etag == "" || aws.ToString(out.ChecksumSHA256) != checksum {
			return abort(fmt.Errorf("upload multipart part %d to s3://%s/%s returned mismatched identity", partNumber, bucket, key))
		}
		parts = append(parts, types.CompletedPart{PartNumber: &partNumber, ETag: &etag, ChecksumSHA256: &checksum})
	}

	checksum := digest.authoritativeChecksum()
	completed, completeErr := c.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &bucket, Key: &key, UploadId: &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}, MpuObjectSize: &digest.size,
		ChecksumSHA256: &checksum, ChecksumType: types.ChecksumTypeComposite,
	})
	if completeErr != nil {
		return abort(fmt.Errorf("complete multipart s3://%s/%s (commit ambiguous): %w", bucket, key, completeErr))
	}
	if completed == nil || aws.ToString(completed.ChecksumSHA256) != checksum || completed.ChecksumType != types.ChecksumTypeComposite {
		return fmt.Errorf("complete multipart s3://%s/%s returned a mismatched SHA-256 checksum or type", bucket, key)
	}
	return nil
}
