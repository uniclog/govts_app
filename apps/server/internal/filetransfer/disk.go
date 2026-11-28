package filetransfer

import "uniclog.io/sonoryx/internal/fileops"

func publishFile(source, destination string) error {
	return fileops.PublishFile(source, destination)
}

func syncDirectory(path string) error {
	return fileops.SyncDirectory(path)
}
