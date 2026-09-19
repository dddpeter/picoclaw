package fstools

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// textEncoding identifies how raw file bytes map to editable text.
type textEncoding uint8

const (
	// encRaw means "not known to be decodable text" (or an unsupported
	// encoding): bytes pass through unchanged.
	encRaw textEncoding = iota
	// encUTF8 is UTF-8 text without a BOM.
	encUTF8
	// encUTF8BOM is UTF-8 text with a leading byte order mark.
	encUTF8BOM
	// encGB18030 is GBK/GB2312-compatible Chinese text (GB18030 is the
	// superset codec used both to decode and re-encode).
	encGB18030
)

// maxDecodeFileSize bounds whole-file decoding in the line-mode reader: files
// up to this size are decoded in memory to present clean text; larger files
// keep the streaming raw path.
const maxDecodeFileSize = 8 << 20 // 8MB

// decodeText converts raw file bytes into editable UTF-8 text and reports the
// detected encoding so encodeText can restore the on-disk form byte-identically.
//
// Detection order: UTF-8 BOM, plain UTF-8, then GB18030 (GBK). Raw bytes
// containing NUL are never transcoded — NUL is a strong binary/UTF-16 signal
// and GB18030 would happily "decode" some such payloads.
func decodeText(raw []byte) (string, textEncoding) {
	if bom, rest := splitUTF8BOM(string(raw)); bom != "" && utf8.ValidString(rest) {
		return rest, encUTF8BOM
	}
	if utf8.Valid(raw) {
		return string(raw), encUTF8
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return string(raw), encRaw
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return string(raw), encRaw
	}
	return string(decoded), encGB18030
}

// encodeText is the inverse of decodeText: it restores the on-disk encoding.
func encodeText(text string, enc textEncoding) ([]byte, error) {
	switch enc {
	case encGB18030:
		encoded, err := simplifiedchinese.GB18030.NewEncoder().String(text)
		if err != nil {
			return nil, fmt.Errorf("failed to re-encode content as GB18030: %w", err)
		}
		return []byte(encoded), nil
	case encUTF8BOM:
		return []byte(utf8BOM + text), nil
	default: // encUTF8, encRaw
		return []byte(text), nil
	}
}

// trimTrailingPartialSequence drops up to three trailing bytes >= 0x80 from a
// sniff sample, so validity checks do not fail merely because the 512-byte
// window cut a multi-byte UTF-8 or GB18030 sequence in half.
func trimTrailingPartialSequence(sample []byte) []byte {
	end := len(sample)
	for end > 0 && sample[end-1] >= 0x80 && len(sample)-end < 3 {
		end--
	}
	return sample[:end]
}
