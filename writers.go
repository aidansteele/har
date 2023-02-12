package har

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type Writer interface {
	WriteEntry(entry json.RawMessage) error
}

var _ Writer = (*HarWriter)(nil)

type HarWriter struct {
	first  bool
	closed bool
	mut    sync.Mutex
	writer io.Writer
}

func NewHarWriter(writer io.Writer, creator *Creator) (*HarWriter, error) {
	var err error
	creatorJson, _ := json.Marshal(creator)

	_, err = writer.Write([]byte(`{"log":{"version":"1.2","creator":`))
	if err != nil {
		return nil, fmt.Errorf("writing preamble: %w", err)
	}

	_, err = writer.Write(creatorJson)
	if err != nil {
		return nil, fmt.Errorf("writing preamble: %w", err)
	}

	_, err = writer.Write([]byte(`,"entries":[` + "\n"))
	if err != nil {
		return nil, fmt.Errorf("writing preamble: %w", err)
	}

	return &HarWriter{
		first:  true,
		writer: writer,
	}, nil
}

func (w *HarWriter) WriteEntry(entry json.RawMessage) error {
	w.mut.Lock()
	defer w.mut.Unlock()

	if w.closed {
		return fmt.Errorf("HarWriter already closed")
	}

	if !w.first {
		_, err := w.writer.Write([]byte(",\n"))
		if err != nil {
			return fmt.Errorf("writing har entry: %w", err)
		}
	}

	w.first = false

	_, err := w.writer.Write(entry)
	if err != nil {
		return fmt.Errorf("writing har entry: %w", err)
	}

	return nil
}

func (w *HarWriter) Close() error {
	w.mut.Lock()
	defer w.mut.Unlock()

	if w.closed {
		return fmt.Errorf("HarWriter already closed")
	}

	w.closed = true
	_, err := w.writer.Write([]byte("\n]}}"))
	if err != nil {
		return fmt.Errorf("closing har writer: %w", err)
	}

	return nil
}

var _ Writer = (*HarNDWriter)(nil)

type HarNDWriter struct {
	mut    sync.Mutex
	writer io.Writer
}

func NewHarNDWriter(writer io.Writer) *HarNDWriter {
	return &HarNDWriter{writer: writer}
}

func (w *HarNDWriter) WriteEntry(entry json.RawMessage) error {
	w.mut.Lock()
	defer w.mut.Unlock()

	_, err := w.writer.Write(entry)
	if err != nil {
		return err
	}

	_, err = w.writer.Write([]byte{'\n'})
	return err
}
