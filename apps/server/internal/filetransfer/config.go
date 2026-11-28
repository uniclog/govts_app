package filetransfer

import (
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/fileapi"
	"uniclog.io/sonoryx/internal/fileops"
)

const ChunkSize = fileapi.ChunkSize

type Config struct {
	Directory            string
	MaxFileBytes         int64
	UserQuotaBytes       int64
	ServerQuotaBytes     int64
	UserBytesPerSecond   int
	ServerBytesPerSecond int
	MaxTransfers         int
	UploadTTL            time.Duration
	Retention            time.Duration
	MinFreeBytes         uint64
	UploadsEnabled       bool
	DownloadsEnabled     bool
	Revision             uint64
}

func DefaultConfig(dir string) Config {
	return Config{Directory: dir, MaxFileBytes: 1 << 30, UserQuotaBytes: 10 << 30, ServerQuotaBytes: 100 << 30, UserBytesPerSecond: 512 << 10, ServerBytesPerSecond: 4 << 20, MaxTransfers: 8, UploadTTL: 24 * time.Hour, MinFreeBytes: 1 << 30, UploadsEnabled: true, DownloadsEnabled: true}
}
func (c Config) Validate() error {
	if c.Directory == "" || c.MaxFileBytes < 1 || c.MaxFileBytes > 1<<40 || c.UserQuotaBytes < c.MaxFileBytes || c.ServerQuotaBytes < c.UserQuotaBytes || c.ServerQuotaBytes > 1<<50 || c.UserBytesPerSecond < 16<<10 || c.ServerBytesPerSecond < c.UserBytesPerSecond || c.ServerBytesPerSecond > 1<<30 || c.MaxTransfers < 1 || c.MaxTransfers > 256 || c.UploadTTL < time.Minute || c.Retention < 0 || c.MinFreeBytes > 1<<50 {
		return errors.New("invalid file transfer configuration")
	}
	return nil
}
func SafeName(name string) string { return fileops.SafeName(name) }
