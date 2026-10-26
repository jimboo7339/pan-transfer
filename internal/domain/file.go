package domain

import (
	"net/http"
	"time"
)

type HashType string

const (
	HashSHA1 HashType = "sha1"
	HashMD5  HashType = "md5"
)

type IDKind uint8

const (
	IDStable IDKind = iota
	IDPath
)

type FileItem struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Size    int64               `json:"size"`
	IsDir   bool                `json:"is_dir"`
	ModTime time.Time           `json:"mod_time"`
	Hash    map[HashType]string `json:"hash,omitempty"`
	Thumb   string              `json:"thumb,omitempty"`
	IDKind  IDKind              `json:"id_kind,omitempty"`
}

type DownloadMode uint8

const (
	DownloadRedirect DownloadMode = iota
	DownloadProxy
)

type UpstreamTransportPolicy uint8

const (
	UpstreamTransportDefault UpstreamTransportPolicy = iota
	UpstreamTransportForceHTTP2
)

type DownloadInfo struct {
	URL         string
	Headers     http.Header
	Mode        DownloadMode
	Expiration  time.Duration
	Concurrency int
	ChunkSize   int64
	// Transfer 参数仅用于完整文件传输；未声明时分别沿用 ChunkSize 和 Concurrency。
	TransferChunkSize   int64
	TransferConcurrency int
	ForceProxy          bool
	TransportPolicy     UpstreamTransportPolicy
	// LocalPath 非空时表示内容在本机文件，播放层直接读盘，无需上游 HTTP。
	LocalPath string

	Size     int64
	FileName string
}
