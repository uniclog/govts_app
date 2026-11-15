package filetransfer

import "uniclog.io/sonoryx/internal/atomicfile"

func publishFile(source, destination string) error { return atomicfile.Publish(source, destination) }

func syncDirectory(path string) error { return atomicfile.SyncDir(path) }
