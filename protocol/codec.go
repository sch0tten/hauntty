package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

// Encoder writes line-delimited JSON messages.
type Encoder struct {
	w io.Writer
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

func (e *Encoder) Encode(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = e.w.Write(data)
	return err
}

// Decoder reads line-delimited JSON messages.
type Decoder struct {
	scanner *bufio.Scanner
}

func NewDecoder(r io.Reader) *Decoder {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max line
	return &Decoder{scanner: scanner}
}

func (d *Decoder) Decode(v any) error {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(d.scanner.Bytes(), v)
}

// SendRequest sends a request on a connection and returns the response.
func SendRequest(conn net.Conn, req *Request) (*Response, error) {
	enc := NewEncoder(conn)
	dec := NewDecoder(conn)

	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return &resp, nil
}
