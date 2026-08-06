package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
)

const DefaultChunkSize int64 = 4 << 20 // 4MB

type Chunk struct {
	Index int
	Start int64
	End   int64 // exclusive
	Total int64
	Data  []byte
	MD5   string
}

func ReadChunks(path string, chunkSize int64) (<-chan Chunk, <-chan error) {
	chunks := make(chan Chunk)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		f, err := os.Open(path)
		if err != nil {
			errs <- err
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			errs <- err
			return
		}

		total := info.Size()
		var start int64
		index := 0

		for start < total {
			end := start + chunkSize
			if end > total {
				end = total
			}

			data := make([]byte, end-start)
			n, err := f.ReadAt(data, start)
			if err != nil && err != io.EOF {
				errs <- err
				return
			}

			data = data[:n]

			h := md5.Sum(data)
			chunks <- Chunk{
				Index: index,
				Start: start,
				End:   end,
				Total: total,
				Data:  data,
				MD5:   hex.EncodeToString(h[:]),
			}

			start = end
			index++
		}
	}()

	return chunks, errs
}

func SendFile(ctx context.Context, path string, uploadURL string, chunkSize int64) error {
	chunks, errs := ReadChunks(path, chunkSize)

	for chunk := range chunks {
		if err := sendChunk(ctx, uploadURL, chunk); err != nil {
			return fmt.Errorf("chunk %d: %w", chunk.Index, err)
		}
	}

	return <-errs
}

func sendChunk(ctx context.Context, uploadURL string, chunk Chunk) error {
	const maxRetries = 3

	for attempt := range maxRetries {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(chunk.Data))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", chunk.Start, chunk.End-1, chunk.Total))
		req.Header.Set("X-Chunk-MD5", chunk.MD5)
		req.Header.Set("Content-Type", "applications/octet-stream")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if attempt == maxRetries-1 {
				return err
			}
			continue
		}
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusPartialContent:
			return nil
		case attempt == maxRetries-1:
			return fmt.Errorf("server error after %d attempts: %s", maxRetries, resp.Status)
		default:
			return fmt.Errorf("server rejected chunk: %s", resp.Status)
		}
	}

	return nil
}
