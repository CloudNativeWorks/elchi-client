package journal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
)

func readAllLines(path string, n uint32) ([]string, error) {
	const readBlockSize = 8192

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	var (
		offset     int64 = fi.Size()
		lineBuffer       = []byte{}
		lines            = []string{}
	)

	for offset > 0 && uint32(len(lines)) < n {
		blockSize := int64(readBlockSize)
		if offset < blockSize {
			blockSize = offset
		}
		offset -= blockSize

		buf := make([]byte, blockSize)
		_, err := file.ReadAt(buf, offset)
		if err != nil {
			return nil, fmt.Errorf("read error: %w", err)
		}

		lineBuffer = append(buf, lineBuffer...)

		scanner := bufio.NewScanner(bytes.NewReader(lineBuffer))
		scanner.Split(bufio.ScanLines)

		tmpLines := []string{}
		for scanner.Scan() {
			tmpLines = append(tmpLines, scanner.Text())
		}

		if uint32(len(tmpLines)) >= n {
			lines = tmpLines[len(tmpLines)-int(n):]
			break
		}

		lines = tmpLines
	}

	return lines, nil
}

func readAllLinesGz(path string, n uint32) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open gz file: %w", err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	scanner := bufio.NewScanner(gzr)
	ring := make([]string, 0, n)

	for scanner.Scan() {
		line := scanner.Text()
		if uint32(len(ring)) < n {
			ring = append(ring, line)
		} else {
			copy(ring, ring[1:])
			ring[len(ring)-1] = line
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	return ring, nil
}

func getOrderedLogFiles(basePath string) []string {
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	var files []string
	for i := 0; i <= 5; i++ {
		var name string
		if i == 0 {
			name = base
		} else {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
		gzPath := path + ".gz"
		if _, err := os.Stat(gzPath); err == nil {
			files = append(files, gzPath)
		}
	}
	return files
}
