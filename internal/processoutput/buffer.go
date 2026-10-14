// Package processoutput bounds output captured from native helper processes.
package processoutput

import (
	"bytes"
	"errors"
)

type Buffer struct {
	buffer bytes.Buffer
	Limit  int
	Err    error
}

func (b *Buffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *Buffer) String() string { return b.buffer.String() }
func (b *Buffer) Len() int       { return b.buffer.Len() }

func (b *Buffer) Write(value []byte) (int, error) {
	if b.Err != nil {
		return 0, b.Err
	}
	if len(value) > b.Limit-b.Len() {
		b.Err = errors.New("process output exceeds limit")
		return 0, b.Err
	}
	return b.buffer.Write(value)
}
