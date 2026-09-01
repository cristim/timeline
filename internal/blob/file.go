package blob

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	defaultSinglePutLimit    int64 = 64 << 20
	defaultMultipartPartSize int64 = 16 << 20
)

type fileUploadConfig struct {
	singlePutLimit    int64
	multipartPartSize int64
}

type fileDigest struct {
	sha256Raw [sha256.Size]byte
	md5Raw    [md5.Size]byte
	size      int64
	partSize  int64
	parts     [][sha256.Size]byte
}

func (d fileDigest) sha256Base64() string { return base64.StdEncoding.EncodeToString(d.sha256Raw[:]) }
func (d fileDigest) sha256Hex() string    { return hex.EncodeToString(d.sha256Raw[:]) }
func (d fileDigest) md5Hex() string       { return hex.EncodeToString(d.md5Raw[:]) }

func (d fileDigest) checksumType() types.ChecksumType {
	if len(d.parts) > 0 {
		return types.ChecksumTypeComposite
	}
	return types.ChecksumTypeFullObject
}

func (d fileDigest) authoritativeChecksum() string {
	if len(d.parts) == 0 {
		return d.sha256Base64()
	}
	h := sha256.New()
	for _, part := range d.parts {
		_, _ = h.Write(part[:])
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(d.parts))
}

// PutFileIfChanged uploads a stable regular file unless S3 already holds the
// same checksum. The path is opened once and is rechecked after the operation.
func (c *Client) PutFileIfChanged(ctx context.Context, bucket, key, path, contentType string) (uploaded bool, err error) {
	file, err := openFileNoFollow(path)
	if err != nil {
		return false, fmt.Errorf("open %s without following links: %w", path, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	initial, err := file.Stat()
	if err != nil || !initial.Mode().IsRegular() {
		return false, fmt.Errorf("stat upload file %s: regular=%t: %w", path, initial != nil && initial.Mode().IsRegular(), err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(initial, pathInfo) {
		return false, fmt.Errorf("upload file %s path identity changed", path)
	}
	uploaded, err = c.PutReaderIfChanged(ctx, bucket, key, file, initial.Size(), contentType)
	if err != nil {
		return false, err
	}
	if err := verifyOpenFileIdentity(path, file, initial); err != nil {
		return false, err
	}
	return uploaded, nil
}

// PutReaderIfChanged uploads a bounded seekable artifact unless S3 already
// holds the same checksum. The caller retains ownership of reader.
func (c *Client) PutReaderIfChanged(ctx context.Context, bucket, key string, reader io.ReaderAt, size int64, contentType string) (uploaded bool, err error) {
	if size < 0 {
		return false, fmt.Errorf("upload reader size must be nonnegative: %d", size)
	}
	config := c.effectiveFileUploadConfig()
	partSize := int64(0)
	if size > config.singlePutLimit {
		partSize = config.multipartPartSize
	}
	digest, err := digestReader(ctx, reader, size, partSize)
	if err != nil {
		return false, fmt.Errorf("hash upload reader: %w", err)
	}

	head, headErr := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &bucket, Key: &key, ChecksumMode: types.ChecksumModeEnabled,
	})
	if headErr == nil && headMatches(head, digest) {
		if err := verifyReader(ctx, reader, digest); err != nil {
			return false, err
		}
		return false, nil
	}
	if headErr != nil && !isNotFound(headErr) {
		return false, fmt.Errorf("head s3://%s/%s: %w", bucket, key, headErr)
	}

	if len(digest.parts) == 0 {
		err = c.putSingleFile(ctx, bucket, key, reader, contentType, digest)
	} else {
		err = c.putMultipartFile(ctx, bucket, key, reader, contentType, digest)
	}
	if err != nil {
		return false, err
	}
	if err := verifyReader(ctx, reader, digest); err != nil {
		return false, err
	}
	return true, nil
}

func headMatches(head *s3.HeadObjectOutput, digest fileDigest) bool {
	if head == nil {
		return false
	}
	if checksum := aws.ToString(head.ChecksumSHA256); checksum != "" {
		return checksum == digest.authoritativeChecksum() && head.ChecksumType == digest.checksumType()
	}
	if hasStoredChecksum(head) {
		return false
	}
	etag := strings.Trim(aws.ToString(head.ETag), `"`)
	return etag != "" && !strings.Contains(etag, "-") && etag == digest.md5Hex()
}

func hasStoredChecksum(head *s3.HeadObjectOutput) bool {
	return head.ChecksumType != "" || aws.ToString(head.ChecksumCRC32) != "" || aws.ToString(head.ChecksumCRC32C) != "" ||
		aws.ToString(head.ChecksumCRC64NVME) != "" || aws.ToString(head.ChecksumMD5) != "" ||
		aws.ToString(head.ChecksumSHA1) != "" || aws.ToString(head.ChecksumSHA512) != "" ||
		aws.ToString(head.ChecksumXXHASH128) != "" || aws.ToString(head.ChecksumXXHASH3) != "" ||
		aws.ToString(head.ChecksumXXHASH64) != ""
}

func (c *Client) putSingleFile(ctx context.Context, bucket, key string, reader io.ReaderAt, contentType string, digest fileDigest) error {
	checksum := digest.sha256Base64()
	out, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &bucket, Key: &key, Body: io.NewSectionReader(reader, 0, digest.size),
		ContentLength: &digest.size, ContentType: &contentType,
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: &checksum,
		Metadata: map[string]string{"sha256": digest.sha256Hex(), "size": strconv.FormatInt(digest.size, 10)},
	})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", bucket, key, err)
	}
	if out == nil || aws.ToString(out.ChecksumSHA256) != checksum || out.ChecksumType != types.ChecksumTypeFullObject {
		return fmt.Errorf("put s3://%s/%s returned a mismatched SHA-256 checksum or type", bucket, key)
	}
	return nil
}

func digestReader(ctx context.Context, reader io.ReaderAt, size, partSize int64) (fileDigest, error) {
	wholeSHA, wholeMD5 := sha256.New(), md5.New()
	buffer := make([]byte, 128*1024)
	digest := fileDigest{partSize: partSize}
	var partHash = sha256.New()
	var partBytes int64
	stream := io.NewSectionReader(reader, 0, size)
	for {
		if err := ctx.Err(); err != nil {
			return fileDigest{}, err
		}
		n, readErr := stream.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			_, _ = wholeSHA.Write(chunk)
			_, _ = wholeMD5.Write(chunk)
			digest.size += int64(n)
			if partSize > 0 {
				for len(chunk) > 0 {
					remaining := partSize - partBytes
					take := int64(len(chunk))
					if take > remaining {
						take = remaining
					}
					_, _ = partHash.Write(chunk[:int(take)])
					chunk = chunk[int(take):]
					partBytes += take
					if partBytes == partSize {
						var sum [sha256.Size]byte
						copy(sum[:], partHash.Sum(nil))
						digest.parts = append(digest.parts, sum)
						partHash.Reset()
						partBytes = 0
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fileDigest{}, readErr
		}
	}
	copy(digest.sha256Raw[:], wholeSHA.Sum(nil))
	copy(digest.md5Raw[:], wholeMD5.Sum(nil))
	if digest.size != size {
		return fileDigest{}, io.ErrUnexpectedEOF
	}
	if partSize > 0 && partBytes > 0 {
		var sum [sha256.Size]byte
		copy(sum[:], partHash.Sum(nil))
		digest.parts = append(digest.parts, sum)
	}
	return digest, nil
}

func verifyReader(ctx context.Context, reader io.ReaderAt, want fileDigest) error {
	got, err := digestReader(ctx, reader, want.size, want.partSize)
	if err != nil {
		return fmt.Errorf("rehash upload reader: %w", err)
	}
	if got.size != want.size || got.sha256Raw != want.sha256Raw || got.md5Raw != want.md5Raw {
		return errors.New("upload reader changed during publication")
	}
	return nil
}

func verifyOpenFileIdentity(path string, file *os.File, initial os.FileInfo) error {
	info, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !os.SameFile(info, initial) || !os.SameFile(pathInfo, initial) ||
		info.Mode() != initial.Mode() || info.Size() != initial.Size() || !info.ModTime().Equal(initial.ModTime()) {
		return fmt.Errorf("upload file %s changed during publication", path)
	}
	return nil
}

func (c *Client) effectiveFileUploadConfig() fileUploadConfig {
	config := c.fileUpload
	if config.singlePutLimit == 0 {
		config.singlePutLimit = defaultSinglePutLimit
	}
	if config.multipartPartSize == 0 {
		config.multipartPartSize = defaultMultipartPartSize
	}
	return config
}
