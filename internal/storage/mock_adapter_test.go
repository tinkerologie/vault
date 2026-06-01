package storage

import (
	"context"
	"errors"
	"io"
)

// mockAdapter is an Adapter whose methods return a not-implemented error, for
// embedding in focused test doubles that override a few methods.
type mockAdapter struct{}

func (mockAdapter) Write(string, io.Reader) error { return errors.New("not impl") }
func (mockAdapter) WriteFrom(string, func() (io.ReadCloser, error)) error {
	return errors.New("not impl")
}
func (mockAdapter) Read(string) (io.ReadCloser, error) { return nil, errors.New("not impl") }
func (mockAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("not impl")
}
func (mockAdapter) Delete(string) error             { return errors.New("not impl") }
func (mockAdapter) List(string) ([]FileInfo, error) { return nil, errors.New("not impl") }
func (mockAdapter) Stat(string) (FileInfo, error)   { return FileInfo{}, errors.New("not impl") }
func (mockAdapter) TestConnection() error           { return errors.New("not impl") }
func (mockAdapter) GetCapacity(context.Context) (Capacity, error) {
	return Capacity{}, errors.New("not impl")
}
func (mockAdapter) Usage() (int64, int64, error) { return 0, 0, errors.New("not impl") }
