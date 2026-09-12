package cellncapability

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

// CanonicalDecision returns the contract's strict RFC 8785-compatible,
// integer-only canonical decision bytes and their typed SHA-256 digest.
func CanonicalDecision(d Decision) ([]byte, string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, "", err
	}
	canonical, err := canonicalizeJSON(raw)
	if err != nil {
		return nil, "", err
	}
	return canonical, sha256Digest(canonical), nil
}

// CanonicalRequest validates and canonicalises an external execution/turn
// request before it is bound into Decision.RequestDigest.
func CanonicalRequest(raw []byte) ([]byte, string, error) {
	canonical, err := canonicalizeJSON(raw)
	if err != nil {
		return nil, "", err
	}
	return canonical, sha256Digest(canonical), nil
}

func canonicalizeJSON(raw []byte) ([]byte, error) {
	if err := checkStrictJSON(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := appendCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func appendCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		appendCanonicalString(out, v)
	case json.Number:
		s := v.String()
		if !isCanonicalInteger(s) {
			return fmt.Errorf("number %q is outside integer-only JCS profile", s)
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > 9007199254740991 || n < -9007199254740991 {
			return fmt.Errorf("number %q is outside JSON safe-integer range", s)
		}
		out.WriteString(s)
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			appendCanonicalString(out, key)
			out.WriteByte(':')
			if err := appendCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
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

func appendCanonicalString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
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

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
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
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	return nil
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
	token, err := dec.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("trailing JSON token %v", token)
}

func walkStrict(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
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
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func rejectLoneSurrogates(raw []byte) error {
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			escaped = false
			if c != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return fmt.Errorf("short unicode escape")
			}
			first, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return err
			}
			i += 4
			if first >= 0xD800 && first <= 0xDBFF {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return fmt.Errorf("lone high surrogate")
				}
				second, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err != nil || second < 0xDC00 || second > 0xDFFF {
					return fmt.Errorf("invalid surrogate pair")
				}
				i += 6
			} else if first >= 0xDC00 && first <= 0xDFFF {
				return fmt.Errorf("lone low surrogate")
			}
			continue
		}
		if c == '\\' {
			escaped = true
		} else if c == '"' {
			inString = false
		}
	}
	if inString {
		return fmt.Errorf("unterminated JSON string")
	}
	return nil
}
