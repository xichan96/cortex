package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	frames := []Frame{
		{Type: FrameRequest, ID: 1, JSON: []byte(`{"method":"initialize"}`)},
		{Type: FrameResponse, ID: 42, JSON: []byte(`{"result":{}}`)},
		{Type: FrameError, ID: 7, JSON: []byte(`{"code":-32601}`)},
		{Type: FrameEvent, ID: 9, JSON: []byte(`{"event":"tools/update"}`)},
		{Type: FrameCancel, ID: 3},
		{Type: FramePing, ID: 0},
	}
	var buf bytes.Buffer
	for _, want := range frames {
		if err := WriteFrame(&buf, want); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if got.Type != want.Type || got.ID != want.ID || !bytes.Equal(got.JSON, want.JSON) {
			t.Fatalf("frame %d = %+v, want %+v", i, got, want)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing read = %v, want EOF", err)
	}
}

func TestReadFrameTruncatedBody(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, Frame{Type: FrameRequest, ID: 1, JSON: []byte(`{"x":1}`)})
	data := buf.Bytes()
	_, err := ReadFrame(bytes.NewReader(data[:len(data)-2]))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
}

func TestReadFrameMalformedLength(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 4)
	if _, err := ReadFrame(bytes.NewReader(header[:])); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("err = %v, want ErrMalformedFrame", err)
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(header[:])); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadFrameLimit(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, Frame{Type: FrameRequest, ID: 1, JSON: []byte(`{"payload":"abcdef"}`)})
	if _, err := ReadFrameLimit(&buf, 6); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestWriteFrameTooLarge(t *testing.T) {
	err := WriteFrame(io.Discard, Frame{Type: FrameRequest, JSON: make([]byte, MaxFrameSize)})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}
