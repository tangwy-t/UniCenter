package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 是 S3 兼容对象存储后端：一套代码覆盖 MinIO / AWS S3 / 阿里云 OSS /
// 腾讯云 COS（均为 S3 协议，endpoint + 凭据配置即可切换）。
type S3 struct {
	client *minio.Client
	bucket string
}

// S3Options 定义 S3 兼容后端的连接参数。
type S3Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	PathStyle bool // true = 路径寻址（自建 MinIO 常需）；false = 虚拟主机/自动
}

// NewS3 构造一个 S3 兼容后端。构造成功即完成参数校验（不建立连接）；
// 实际连通性在首次 Put/Open 时体现。
func NewS3(opts S3Options) (*S3, error) {
	if opts.Endpoint == "" || opts.AccessKey == "" || opts.SecretKey == "" || opts.Bucket == "" {
		return nil, errors.New("storage/s3: endpoint/accessKey/secretKey/bucket 不能为空")
	}
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure:       opts.UseSSL,
		Region:       opts.Region,
		BucketLookup: bucketLookup(opts.PathStyle),
	})
	if err != nil {
		return nil, fmt.Errorf("storage/s3: init client: %w", err)
	}
	return &S3{client: client, bucket: opts.Bucket}, nil
}

func bucketLookup(pathStyle bool) minio.BucketLookupType {
	if pathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

func (s *S3) Put(ctx context.Context, key string, src io.Reader, maxSize int64) (int64, error) {
	cr := &countingReader{r: src}
	// size 传 -1 让 minio-go 走流式上传；LimitReader(maxSize+1) —— 若内容实际
	// 超出，计数会溢出上限，上传完成后回滚对象并报错。
	_, err := s.client.PutObject(ctx, s.bucket, key, io.LimitReader(cr, maxSize+1), -1, minio.PutObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("storage/s3: put: %w", err)
	}
	if cr.n > maxSize {
		_ = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
		return 0, ErrTooLarge
	}
	return cr.n, nil
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage/s3: stat: %w", err)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage/s3: get: %w", err)
	}
	return &s3Seeker{obj: obj, size: info.Size}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage/s3: delete: %w", err)
	}
	return nil
}

// s3Seeker 把 minio.Object 适配为 io.ReadSeekCloser，并显式实现 SeekEnd
// （minio.Object 的 SeekEnd 依赖 Stat，行为依版本有差异；这里用 Stat 到的
// size 封死，保证 http.ServeContent 的 sizeFunc 可用）。
type s3Seeker struct {
	obj  *minio.Object
	size int64
}

func (s *s3Seeker) Read(p []byte) (int, error) { return s.obj.Read(p) }
func (s *s3Seeker) Close() error               { return s.obj.Close() }

func (s *s3Seeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		return s.obj.Seek(offset, io.SeekStart)
	case io.SeekCurrent:
		return s.obj.Seek(offset, io.SeekCurrent)
	case io.SeekEnd:
		return s.obj.Seek(s.size+offset, io.SeekStart)
	default:
		return 0, errors.New("storage/s3: invalid whence")
	}
}

// countingReader 统计实际读取的字节数。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// isNoSuchKey 判断 minio 错误是否为「对象不存在」。
func isNoSuchKey(err error) bool {
	e := minio.ToErrorResponse(err)
	return e.Code == "NoSuchKey" || e.Code == "NoSuchObject"
}
