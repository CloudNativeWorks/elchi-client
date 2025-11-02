package journal

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
)

func readAllLines(path string, n uint32) ([]string, error) {
	const readBlockSize = 8192
	const maxLineBufferSize = 1024 * 1024 // 1MB limit to prevent infinite buffer growth

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
		offset         int64 = fi.Size()
		lineBuffer           = []byte{}
		lines                = []string{}
		lastLineCount        = 0
		iterationCount       = 0
	)

	const maxIterations = 1000 // Safety limit to prevent infinite loops

	for offset > 0 && uint32(len(lines)) < n {
		iterationCount++
		if iterationCount > maxIterations {
			fmt.Printf("WARNING: Exceeded max iterations (%d), breaking\n", maxIterations)
			break
		}

		// Re-check file size in case of rotation
		currentFi, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("failed to re-stat file: %w", err)
		}

		// If file was rotated/truncated, break
		if currentFi.Size() != fi.Size() {
			fmt.Printf("WARNING: File size changed during read (rotation?), was %d, now %d bytes\n",
				fi.Size(), currentFi.Size())
			break
		}

		blockSize := int64(readBlockSize)
		if offset < blockSize {
			blockSize = offset
		}
		offset -= blockSize

		buf := make([]byte, blockSize)
		_, err = file.ReadAt(buf, offset)
		if err != nil {
			return nil, fmt.Errorf("read error: %w", err)
		}

		lineBuffer = append(buf, lineBuffer...)

		// Safety check: if buffer grows too large, break to prevent infinite loop
		if len(lineBuffer) > maxLineBufferSize {
			fmt.Printf("WARNING: lineBuffer exceeded %d bytes, breaking to prevent infinite loop\n", maxLineBufferSize)
			break
		}

		scanner := bufio.NewScanner(bytes.NewReader(lineBuffer))
		// Increase scanner buffer to handle longer lines
		scannerBuf := make([]byte, 0, 256*1024) // 256KB buffer
		scanner.Buffer(scannerBuf, 256*1024)
		scanner.Split(bufio.ScanLines)

		tmpLines := []string{}
		for scanner.Scan() {
			tmpLines = append(tmpLines, scanner.Text())
		}

		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			fmt.Printf("WARNING: Scanner error: %v, breaking\n", err)
			break
		}

		// Check if we're making progress (finding new lines)
		if len(tmpLines) == lastLineCount && offset > 0 {
			// No new lines found despite reading more data - likely a very long line
			// Keep the partial data and continue, but don't infinite loop
			if len(lineBuffer) > readBlockSize*2 {
				fmt.Printf("WARNING: No new lines found, buffer size: %d bytes, breaking\n", len(lineBuffer))
				break
			}
		}
		lastLineCount = len(tmpLines)

		if uint32(len(tmpLines)) >= n {
			lines = tmpLines[len(tmpLines)-int(n):]
			break
		}

		lines = tmpLines
	}

	return lines, nil
}
