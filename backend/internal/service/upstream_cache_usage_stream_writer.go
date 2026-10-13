package service

import "io"

// cacheUsageStreamWriter records failed output without changing the existing
// stream's response or billing drain policy. A completed upstream frame alone
// cannot make a disconnected business stream a valid cache sample.
type cacheUsageStreamWriter struct {
	writer io.Writer
	failed bool
}

func (w *cacheUsageStreamWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.failed = w.failed || err != nil || n != len(p)
	return n, err
}

func (w *cacheUsageStreamWriter) WriteString(s string) (int, error) {
	n, err := io.WriteString(w.writer, s)
	w.failed = w.failed || err != nil || n != len(s)
	return n, err
}
