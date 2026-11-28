package filetransfer

import (
	"bytes"
	"context"
	"errors"
	_ "golang.org/x/image/webp"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"time"
	"uniclog.io/sonoryx/apps/server/internal/persist"
)

// Thumbnail mode executes before flags or listeners, in a killable child process.
func ThumbnailMode() bool {
	if len(os.Args) != 3 || os.Args[1] != "--file-thumbnail" {
		return false
	}
	debug.SetMemoryLimit(256 << 20)
	if err := makeThumbnail(os.Args[2], os.Stdout); err != nil {
		os.Exit(1)
	}
	return true
}
func makeThumbnail(path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(io.LimitReader(f, 32<<20))
	if err != nil {
		return err
	}
	if (format != "jpeg" && format != "png" && format != "webp") || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return errors.New("image dimensions or format unsupported")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	img, _, err := image.Decode(io.LimitReader(f, 32<<20))
	if err != nil {
		return err
	}
	width, height := cfg.Width, cfg.Height
	if width > 320 || height > 320 {
		if width >= height {
			height = max(1, height*320/width)
			width = 320
		} else {
			width = max(1, width*320/height)
			height = 320
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := img.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dst.Set(x, y, img.At(bounds.Min.X+x*cfg.Width/width, bounds.Min.Y+y*cfg.Height/height))
		}
	}
	return jpeg.Encode(out, dst, &jpeg.Options{Quality: 80})
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 512<<10 {
		return 0, errors.New("preview too large")
	}
	return b.Buffer.Write(p)
}
func (s *Server) thumbnail(ctx context.Context, a persist.FileAttachment) ([]byte, error) {
	return runThumbnail(ctx, s.blobPath(a.SHA256))
}
func runThumbnail(ctx context.Context, path string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--file-thumbnail", path)
	hideProcess(cmd)
	var out boundedOutput
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
