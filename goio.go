// MIT License
//
// Copyright (c) 2020 Thomas Mathews
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// This is a simple package that I use for reading and writing data across streams. It also has some helper utilities
// for creating a TLS server and such. If you have any questions send me a ping. I use this for most of my projects as
// I prefer to use my own networking language rather than HTTP.

package goio

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
)

// This is the delimiter we use for commands in the protocol. The other way to determine how to read is by declaring
// size of data being transferred ahead of time while being in context of an action. Using null byte allows us to use
// newlines for our inputs without disruptions.
const (
	NullMarker = 0x0
	StatusOK   = 0x0
)

var (
	ReCommand               = regexp.MustCompile(`^[A-Z]+$`)
	ErrInvalidCommandFormat = errors.New("invalid command format")
)

type StatusError struct {
	Code    byte
	Message string
}

func (e *StatusError) Error() string {
	return e.Message
}

// Checks if the error is one of the connection closing by error or on purpose.
func IsClosed(err error) bool {
	if _, ok := err.(*net.OpError); ok {
		return true
	}
	return err == io.EOF || err == io.ErrUnexpectedEOF
}

// This simply scans the payload size first (uint64) and then copies over the data of expected size to the
// provided writer. This is mainly useful for transferring files. It returned bytes does not include the 8 bytes allocated
// for the size; you can see below you can add 8 to it if you are counting.
func ReadN(r io.Reader, w io.Writer) (int64, error) {
	size, err := ReadSize(r)
	if err != nil {
		return 0, err
	}
	return io.CopyN(w, r, int64(size))
}

func ReadBytes(r io.Reader) ([]byte, error) {
	size, err := ReadSize(r)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	if size <= 0 {
		return buf, nil
	}
	_, err = r.Read(buf)
	return buf, err
}

// Reads chunked data (frame) from the reader until a 0 size frame is declared. We can't return size because it could
// potentially go over the limit of int. I would suggest you track it yourself with a multi writer.
func ReadStream(r io.ReadWriter, w io.Writer) error {
	var buf []byte
	var err error
	for {
		buf, err = ReadBytes(r)
		if err != nil {
			break
		}
		if len(buf) == 0 {
			break
		}
		_, err = w.Write(buf)
		if err != nil {
			break
		}
	}
	return err
}

func ReadSize(r io.Reader) (uint64, error) {
	res := make([]byte, 8)
	if _, err := r.Read(res); err != nil {
		return 0, err
	}
	size := binary.BigEndian.Uint64(res)
	return size, nil
}

func ReadUntilByte(r io.Reader, b byte) ([]byte, error) {
	var buf []byte
	for {
		chunk := make([]byte, 1)
		if _, err := r.Read(chunk); err != nil {
			return buf, err
		}
		buf = append(buf, chunk...)
		if len(buf) >= 1 {
			if buf[len(buf)-1:][0] == b {
				//fmt.Printf("READ: '%v'\n", buf)
				return buf[:len(buf)-1], nil
			}
		}
	}
}

func ReadUntilNull(r io.Reader) ([]byte, error) {
	return ReadUntilByte(r, NullMarker)
}

func ReadStringUntilNull(r io.Reader) (string, error) {
	b, err := ReadUntilNull(r)
	return string(b), err
}

func WriteSize(w io.Writer, size int) error {
	sizeBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(sizeBuf, uint64(size))
	if _, err := w.Write(sizeBuf); err != nil {
		return err
	}
	return nil
}

func WriteBytes(w io.Writer, p []byte) error {
	if err := WriteSize(w, len(p)); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

func WriteN(w io.Writer, r io.Reader, size int) error {
	if err := WriteSize(w, size); err != nil {
		return err
	}
	_, err := io.Copy(w, r)
	return err
}

type StreamWriter struct {
	writer io.Writer
}

func (sw *StreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := WriteBytes(sw.writer, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (sw *StreamWriter) Terminate() error {
	return WriteSize(sw.writer, 0)
}

func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{writer: w}
}

// Reads the first byte and checks if it's null. If it's not it means the command failed. If the command did fail
// then we proceed to read the connection until the next null byte, this is the error message. When this method returns
// nil it means that the status was StatusOK.
func ReadStatus(r io.Reader) error {
	res := make([]byte, 1)
	if _, err := r.Read(res); err != nil {
		return err
	}
	if res[0] == StatusOK {
		return nil
	}
	message, err := ReadStringUntilNull(r)
	if err != nil {
		return err
	}
	return &StatusError{Code: res[0], Message: message}
}

// Do the thing.
func Ok(w io.Writer) error {
	_, err := w.Write([]byte{StatusOK})
	return err
}

// Writing a non-OK status is different because there is an optional message attached to the status code. This means
// that the status is written followed by a string followed by a non-optional NullMarker. The message should obviously
// not contain any NullMarker in it.
func NotOk(w io.Writer, code byte, message string) error {
	buf := []byte{code}
	buf = append(buf, []byte(message)...)
	buf = append(buf, NullMarker)
	_, err := w.Write(buf)
	return err
}

func Command(rw io.ReadWriter, command string, args string) error {
	if err := WriteCommand(rw, command, args); err != nil {
		return err
	}
	return ReadStatus(rw)
}

func WriteCommand(w io.Writer, command string, args string) error {
	// TODO clean null bytes from command and args
	buf := []byte(command)
	if len(args) > 0 {
		buf = append(buf, " "+strings.TrimSpace(args)...)
	}
	buf = append(buf, NullMarker)
	_, err := w.Write(buf)
	return err
}

func ReadCommand(r io.Reader) (string, []byte, error) {
	// TBH there is probably a better way to read this without using string/[]byte conversions but I'll figure it out
	// later. I don't think it costs anything to switch between the two tho. It just might be "cooler" code.
	str, err := ReadStringUntilNull(r)
	if err != nil {
		return "", []byte{}, err
	}

	xs := strings.SplitN(str, " ", 2)
	cmd := xs[0]

	if !ReCommand.MatchString(cmd) {
		return "", []byte{}, ErrInvalidCommandFormat
	}

	var input string
	if len(xs) >= 2 {
		input = xs[1]
	}
	return cmd, []byte(input), nil
}

// This is just a little lion to help you see whats going on.
type Rawr struct {
	Name string
}

func (r *Rawr) dumb(p []byte, mode string) (int, error) {
	fmt.Printf("%s(%s): %v\n", r.Name, mode, string(p))
	return len(p), nil
}
func (r *Rawr) Read(p []byte) (int, error) {
	return r.dumb(p, "Read")
}
func (r *Rawr) Write(p []byte) (int, error) {
	return r.dumb(p, "Write")
}

// Just a simple TLS server to help get you up and running. If you really need to customize your TLS server connection
// please don't use this.
type Server struct {
	Conf        *tls.Config
	Certificate tls.Certificate
}

func (s *Server) LoadCert(cert, key string) error {
	var err error
	s.Certificate, err = tls.LoadX509KeyPair(cert, key)
	return err
}

func (s *Server) Listen(address string, insecure bool) (net.Listener, error) {
	s.Conf = &tls.Config{
		ClientAuth:         tls.RequireAnyClientCert,
		Certificates:       []tls.Certificate{s.Certificate},
		InsecureSkipVerify: insecure,
		Rand:               rand.Reader,
	}
	return tls.Listen("tcp", address, s.Conf)
}
