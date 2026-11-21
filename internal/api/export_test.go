package api

import (
	"errors"
	"os"
	"time"
)

// SetStreamWriteTimeout shortens the stream's write deadline for a test
// and returns a function that restores it.
func SetStreamWriteTimeout(d time.Duration) func() {
	old := streamWriteTimeout
	streamWriteTimeout = d
	return func() { streamWriteTimeout = old }
}

// SetReadTimeout shortens the loopback listeners' read deadline for a test
// and returns a function that restores it.
func SetReadTimeout(d time.Duration) func() {
	old := readTimeoutDuration
	readTimeoutDuration = d
	return func() { readTimeoutDuration = old }
}

// ReadTimeout is readTimeout, for a test.
var ReadTimeout = readTimeout

// SetListenRetry shortens the back-off of an optional listener for a test
// and returns a function that restores it.
func SetListenRetry(minDelay, maxDelay time.Duration) func() {
	oldMin, oldMax := listenRetryMin, listenRetryMax
	listenRetryMin, listenRetryMax = minDelay, maxDelay
	return func() { listenRetryMin, listenRetryMax = oldMin, oldMax }
}

// FailUITokenWrite makes writing a new UI token fail after its first byte
// for a test and returns a function that restores it.
func FailUITokenWrite() func() {
	old := writeUIToken
	writeUIToken = func(f *os.File, s string) (int, error) {
		n, _ := f.WriteString(s[:1])
		return n, errors.New("disk full")
	}
	return func() { writeUIToken = old }
}
