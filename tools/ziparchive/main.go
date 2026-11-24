// ziparchive writes a deterministic release ZIP from an explicit allowlist.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) != 9 {
		fatal("usage: ziparchive DIR OUTPUT EPOCH LICENSE README.md SECURITY.md VERSION runnerctl.exe")
	}
	dir, output := os.Args[1], os.Args[2]
	epoch, err := strconv.ParseInt(os.Args[3], 10, 64)
	if err != nil {
		fatal("invalid epoch")
	}
	stamp := time.Unix(epoch, 0).UTC()
	if stamp.Year() < 1980 {
		stamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	f, err := os.Create(output)
	if err != nil {
		fatal(err.Error())
	}
	z := zip.NewWriter(f)
	for _, name := range os.Args[4:] {
		if name != "LICENSE" && name != "README.md" && name != "SECURITY.md" && name != "VERSION" && name != "runnerctl.exe" {
			fatal("unexpected archive entry")
		}
		input, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			fatal(err.Error())
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetModTime(stamp)
		h.SetMode(0644)
		if name == "runnerctl.exe" {
			h.SetMode(0755)
		}
		entry, err := z.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(entry, input)
		}
		input.Close()
		if err != nil {
			fatal(err.Error())
		}
	}
	if err := z.Close(); err != nil {
		fatal(err.Error())
	}
	if err := f.Close(); err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
