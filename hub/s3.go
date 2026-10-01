// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"fmt"
	"io"
)

// S3BlobStore 是预留的 S3 blob 驱动（驱动接口实现点）。
// 当前未实现：所有方法返回 ErrUnsupported，保证 Registry 可用而 S3 能力
// 在不依赖 SDK 的前提下保持"可插拔"。实现时在此接入对象存储的
// GET/PUT/HEAD/DELETE/ListObjectsV2。
type S3BlobStore struct {
	// Bucket 是桶名（预留）。
	Bucket string
	// Endpoint 是 S3 端点（预留）。
	Endpoint string
}

// NewS3BlobStore 返回一个占位的 S3 驱动（未实现）。
func NewS3BlobStore(bucket, endpoint string) (*S3BlobStore, error) {
	if bucket == "" || endpoint == "" {
		return nil, fmt.Errorf("S3 驱动需要 bucket 与 endpoint: %w", ErrBadRequest)
	}
	return &S3BlobStore{Bucket: bucket, Endpoint: endpoint}, nil
}

func (s *S3BlobStore) Put(digest string, r io.Reader, size int64) error {
	return ErrUnsupported
}
func (s *S3BlobStore) Get(digest string) (io.ReadCloser, int64, error) {
	return nil, 0, ErrUnsupported
}
func (s *S3BlobStore) Has(digest string) (bool, error) { return false, ErrUnsupported }
func (s *S3BlobStore) Delete(digest string) error      { return ErrUnsupported }
func (s *S3BlobStore) List() ([]string, error)         { return nil, ErrUnsupported }
