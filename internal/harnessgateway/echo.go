package harnessgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
)

var errEcho = errors.New("response echoes a credential")

// echoWriter passes a response through and refuses bytes that contain a
// secret. It holds back the last len(secret)-1 bytes of each chunk, so a secret
// split across two chunks is still caught. This matches exact bytes. It is not
// a claim to detect arbitrary encodings.
type echoWriter struct {
	out     io.Writer
	flush   func()
	before  func() // runs once, before the first byte is written
	secrets [][]byte
	keep    int
	carry   []byte
	started bool
}

func newEchoWriter(out io.Writer, flush, before func(), secrets []string) *echoWriter {
	w := &echoWriter{out: out, flush: flush, before: before}
	for _, secret := range secrets {
		w.secrets = append(w.secrets, []byte(secret))
		w.keep = max(w.keep, len(secret)-1)
	}
	return w
}

func (w *echoWriter) Write(chunk []byte) (int, error) {
	w.carry = append(w.carry, chunk...)
	for _, secret := range w.secrets {
		if bytes.Contains(w.carry, secret) {
			return 0, errEcho
		}
	}
	if send := len(w.carry) - w.keep; send > 0 {
		if err := w.emit(w.carry[:send]); err != nil {
			return 0, err
		}
		w.carry = append(w.carry[:0], w.carry[send:]...)
	}
	return len(chunk), nil
}

// Close writes what was held back. The caller has reached the end of the body.
func (w *echoWriter) Close() error {
	err := w.emit(w.carry)
	w.carry = nil
	return err
}

func (w *echoWriter) emit(data []byte) error {
	if !w.started {
		w.started = true
		w.before()
	}
	if len(data) == 0 {
		return nil
	}
	if _, err := w.out.Write(data); err != nil {
		return err
	}
	w.flush()
	return nil
}

// echoForms is the secret as it is, and as a JSON encoder writes it. Celln's
// containsResponseSecret checks decoded JSON so that escaping can't hide a key. A
// stream can't be decoded whole, so this checks the escaped forms instead. It is
// not a claim to detect arbitrary encodings, or a key spread over several deltas.
func echoForms(secret string) []string {
	forms := []string{secret}
	add := func(form string) {
		if !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	for _, escapeHTML := range []bool{true, false} {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(escapeHTML)
		if encoder.Encode(secret) != nil {
			continue
		}
		form := strings.TrimSuffix(encoded.String(), "\n")
		form = form[1 : len(form)-1]
		add(form)
		add(strings.ReplaceAll(form, "/", `\/`))
	}
	return forms
}

func headerEchoes(header http.Header, secrets []string) bool {
	for _, values := range header {
		for _, value := range values {
			for _, secret := range secrets {
				if bytes.Contains([]byte(value), []byte(secret)) {
					return true
				}
			}
		}
	}
	return false
}
