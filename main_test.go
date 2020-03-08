package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// Borrowed from - https://medium.com/@hau12a1/golang-capturing-log-println-and-fmt-println-output-770209c791b4
// NOTE: NOT threadsafe due to swapping global os.Stdout/Stderr values
func captureOutput(f func(), captureStderr bool) string {
	reader, writer, err := os.Pipe()
	if err != nil {
		panic(err)
	}

	// Save the current stdout and stderr, restore them on return
	stdout := os.Stdout
	stderr := os.Stderr
	defer func() {
		os.Stdout = stdout
		os.Stderr = stderr
		log.SetOutput(os.Stderr)
	}()

	// Capture the stdout and optionally stderr
	os.Stdout = writer
	if captureStderr {
		os.Stderr = writer
	}

	// Switch the logger to use the writer
	log.SetOutput(writer)
	out := make(chan string)
	wg := new(sync.WaitGroup)
	wg.Add(1)
	go func() {
		var buf bytes.Buffer
		wg.Done()
		if _, err := io.Copy(&buf, reader); err != nil {
			panic(err)
		}
		out <- buf.String()
	}()
	wg.Wait()
	f()
	writer.Close()
	return <-out
}

func TestCaptureOutput(t *testing.T) {
	// Capture just stdout
	out := captureOutput(func() {
		fmt.Println("capture stdout")
		os.Stderr.WriteString("capture stderr\n")
	}, false)

	if strings.Contains(out, "capture stderr") {
		t.Fatal("Should not contain stderr")
	}
	if !strings.Contains(out, "capture stdout") {
		t.Fatal("Missing stdout only")
	}

	// Capture both stdout and stderr
	out = captureOutput(func() {
		fmt.Println("capture stdout")
		os.Stderr.WriteString("capture stderr\n")
	}, true)

	if !strings.Contains(out, "capture stderr") {
		t.Fatal("Missing stderr with stdout")
	}
	if !strings.Contains(out, "capture stdout") {
		t.Fatal("Missing stdout with stderr")
	}
}

func TestLogDebug(t *testing.T) {
	// test with default config, no output

	// set global debug to true
	// check output

}

func TestReadConfig(t *testing.T) {

}
