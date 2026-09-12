package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// canonicalizeJSON implements the RFC 8785 ordering/string rules for this contract's
// deliberately integer-only I-JSON subset. Strict validation runs first so duplicate
// keys, trailing JSON, invalid UTF-8, lone surrogates, floats/exponents and -0 refuse.
func canonicalizeJSON(raw []byte) ([]byte, error) {
	if err := checkStrictJSON(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := appendCanonical(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func appendCanonical(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		appendCanonicalString(b, t)
	case json.Number:
		s := t.String()
		if !isCanonicalInteger(s) {
			return fmt.Errorf("number %q is outside integer-only JCS profile", s)
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > 9007199254740991 || n < -9007199254740991 {
			return fmt.Errorf("number out of safe range")
		}
		b.WriteString(s)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := appendCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			appendCanonicalString(b, k)
			b.WriteByte(':')
			if err := appendCanonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", v)
	}
	return nil
}
func isCanonicalInteger(s string) bool {
	if s == "" || s == "-0" {
		return false
	}
	i := 0
	if s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	if s[i] == '0' && len(s)-i > 1 {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
func appendCanonicalString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
func lessUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
func sha256Digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func checkStrictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF-8")
	}
	if err := rejectLoneSurrogates(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkStrict(dec); err != nil {
		return err
	}
	tok, err := dec.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("trailing JSON token %v", tok)
}
func walkStrict(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			k0, err := dec.Token()
			if err != nil {
				return err
			}
			k, ok := k0.(string)
			if !ok {
				return fmt.Errorf("object key not string")
			}
			if seen[k] {
				return fmt.Errorf("duplicate JSON key %q", k)
			}
			seen[k] = true
			if err := walkStrict(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walkStrict(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter")
	}
}
func rejectLoneSurrogates(raw []byte) error {
	in := false
	esc := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !in {
			if c == '"' {
				in = true
			}
			continue
		}
		if esc {
			esc = false
			if c == 'u' {
				if i+4 >= len(raw) {
					return fmt.Errorf("short unicode escape")
				}
				v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
				if err != nil {
					return err
				}
				i += 4
				if v >= 0xD800 && v <= 0xDBFF {
					if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
						return fmt.Errorf("lone high surrogate")
					}
					w, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
					if err != nil || w < 0xDC00 || w > 0xDFFF {
						return fmt.Errorf("invalid surrogate pair")
					}
					i += 6
				} else if v >= 0xDC00 && v <= 0xDFFF {
					return fmt.Errorf("lone low surrogate")
				}
			}
			continue
		}
		if c == '\\' {
			esc = true
		} else if c == '"' {
			in = false
		}
	}
	if in {
		return fmt.Errorf("unterminated string")
	}
	return nil
}
func strictDecode(raw []byte, target any) error {
	if err := checkStrictJSON(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var x any
	if err := dec.Decode(&x); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
