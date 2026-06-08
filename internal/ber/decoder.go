package ber

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

var (
	ErrTruncated       = errors.New("ber: data truncated")
	ErrInvalidLength   = errors.New("ber: invalid length encoding")
	ErrUnsupportedTag  = errors.New("ber: unsupported tag")
	ErrOverflow        = errors.New("ber: value overflow")
)

const (
	ClassUniversal        = 0x00
	ClassApplication      = 0x40
	ClassContextSpecific  = 0x80
	ClassPrivate          = 0xC0
)

const (
	TagEOC           = 0x00
	TagBoolean       = 0x01
	TagInteger       = 0x02
	TagBitString     = 0x03
	TagOctetString   = 0x04
	TagNull          = 0x05
	TagOID           = 0x06
	TagUTF8String    = 0x0C
	TagSequence      = 0x10
	TagSet           = 0x11
	TagPrintableString = 0x13
	TagVisibleString = 0x1A
	TagUTCTime       = 0x17
	TagGeneralizedTime = 0x18
)

type TagClass byte

func (c TagClass) String() string {
	switch c {
	case ClassUniversal:
		return "Universal"
	case ClassApplication:
		return "Application"
	case ClassContextSpecific:
		return "Context"
	case ClassPrivate:
		return "Private"
	default:
		return "Unknown"
	}
}

type TLV struct {
	Class      byte
	Constructed bool
	Tag        uint64
	Length     int64
	Value      []byte
	Children   []*TLV
}

func (t *TLV) IsConstructed() bool {
	return t.Constructed
}

func (t *TLV) TagCode() byte {
	return t.Class | byte(t.Tag&0x1F)
}

func (t *TLV) String() string {
	if t.Constructed {
		return fmt.Sprintf("TLV{class=%s, tag=%d, len=%d, children=%d}",
			TagClass(t.Class), t.Tag, t.Length, len(t.Children))
	}
	return fmt.Sprintf("TLV{class=%s, tag=%d, len=%d, value=%x}",
		TagClass(t.Class), t.Tag, t.Length, t.Value)
}

func Decode(data []byte) (*TLV, int, error) {
	return decodeTLV(data, 0)
}

func DecodeAll(data []byte) ([]*TLV, error) {
	var result []*TLV
	offset := 0
	for offset < len(data) {
		tlv, consumed, err := decodeTLV(data, offset)
		if err != nil {
			return nil, err
		}
		result = append(result, tlv)
		offset += consumed
	}
	return result, nil
}

func decodeTLV(data []byte, offset int) (*TLV, int, error) {
	if offset >= len(data) {
		return nil, 0, ErrTruncated
	}

	start := offset

	class := data[offset] & 0xC0
	constructed := (data[offset] & 0x20) != 0
	tagNum := uint64(data[offset] & 0x1F)
	offset++

	if tagNum == 0x1F {
		tagNum = 0
		for {
			if offset >= len(data) {
				return nil, 0, ErrTruncated
			}
			b := data[offset]
			offset++
			tagNum = (tagNum << 7) | uint64(b&0x7F)
			if b&0x80 == 0 {
				break
			}
		}
	}

	if offset >= len(data) {
		return nil, 0, ErrTruncated
	}

	length, lengthBytes, err := decodeLength(data, offset)
	if err != nil {
		return nil, 0, err
	}
	offset += lengthBytes

	var value []byte
	if length >= 0 {
		if offset+int(length) > len(data) {
			return nil, 0, ErrTruncated
		}
		value = data[offset : offset+int(length)]
	}

	consumed := offset - start + int(length)

	tlv := &TLV{
		Class:       class,
		Constructed: constructed,
		Tag:         tagNum,
		Length:      length,
		Value:       value,
	}

	if constructed {
		children, err := DecodeAll(value)
		if err != nil && err != io.EOF {
			tlv.Children = children
		} else if err == nil {
			tlv.Children = children
		}
	}

	return tlv, consumed, nil
}

func decodeLength(data []byte, offset int) (int64, int, error) {
	if offset >= len(data) {
		return 0, 0, ErrTruncated
	}

	first := data[offset]
	if first&0x80 == 0 {
		return int64(first), 1, nil
	}

	numBytes := first & 0x7F
	if numBytes == 0 {
		return -1, 1, nil
	}

	if numBytes > 8 {
		return 0, 0, ErrInvalidLength
	}

	if offset+int(numBytes)+1 > len(data) {
		return 0, 0, ErrTruncated
	}

	var length int64
	for i := byte(0); i < numBytes; i++ {
		length = (length << 8) | int64(data[offset+1+int(i)])
	}

	return length, int(numBytes) + 1, nil
}

func (t *TLV) ParseBoolean() (bool, error) {
	if len(t.Value) < 1 {
		return false, ErrTruncated
	}
	return t.Value[0] != 0, nil
}

func (t *TLV) ParseInteger() (int64, error) {
	if len(t.Value) == 0 {
		return 0, nil
	}

	var val int64
	if t.Value[0]&0x80 != 0 {
		val = -1
	}
	for _, b := range t.Value {
		val = (val << 8) | int64(b)
	}
	return val, nil
}

func (t *TLV) ParseUnsigned() (uint64, error) {
	if len(t.Value) == 0 {
		return 0, nil
	}
	var val uint64
	for _, b := range t.Value {
		val = (val << 8) | uint64(b)
	}
	return val, nil
}

func (t *TLV) ParseFloat32() (float32, error) {
	if len(t.Value) != 4 {
		return 0, fmt.Errorf("ber: expected 4 bytes for float32, got %d", len(t.Value))
	}
	bits := binary.BigEndian.Uint32(t.Value)
	return math.Float32frombits(bits), nil
}

func (t *TLV) ParseFloat64() (float64, error) {
	if len(t.Value) != 8 {
		return 0, fmt.Errorf("ber: expected 8 bytes for float64, got %d", len(t.Value))
	}
	bits := binary.BigEndian.Uint64(t.Value)
	return math.Float64frombits(bits), nil
}

func (t *TLV) ParseMMSFloat() (float64, error) {
	if len(t.Value) < 1 {
		return 0, ErrTruncated
	}

	exponentWidth := int(t.Value[0]>>4) & 0x0F
	if exponentWidth == 0 || exponentWidth > 4 {
		return 0, fmt.Errorf("ber: invalid MMS float exponent width %d", exponentWidth)
	}

	sign := t.Value[0] & 0x40
	mantissaWidth := len(t.Value) - 1 - exponentWidth
	if mantissaWidth < 0 {
		return 0, ErrTruncated
	}

	expBytes := t.Value[1 : 1+exponentWidth]
	var expVal int64
	if expBytes[0]&0x80 != 0 {
		for i := range expBytes {
			expBytes[i] = ^expBytes[i]
		}
		for i := len(expBytes) - 1; i >= 0; i-- {
			expBytes[i]++
			if expBytes[i] != 0 {
				break
			}
		}
		for _, b := range expBytes {
			expVal = (expVal << 8) | int64(b)
		}
		expVal = -expVal
	} else {
		for _, b := range expBytes {
			expVal = (expVal << 8) | int64(b)
		}
	}

	mantissaStart := 1 + exponentWidth
	mantissaBytes := t.Value[mantissaStart:]
	var mantissaVal uint64
	for _, b := range mantissaBytes {
		mantissaVal = (mantissaVal << 8) | uint64(b)
	}

	mantissaBits := mantissaWidth * 8
	result := float64(mantissaVal) / float64(uint64(1)<<mantissaBits)
	if sign != 0 {
		result = -result
	}

	for i := int64(0); i < expVal; i++ {
		result *= 2
	}
	for i := int64(0); i > expVal; i-- {
		result /= 2
	}

	return result, nil
}

func (t *TLV) ParseBitString() (unusedBits byte, bits []byte, err error) {
	if len(t.Value) < 1 {
		return 0, nil, ErrTruncated
	}
	return t.Value[0], t.Value[1:], nil
}

func (t *TLV) ParseOctetString() ([]byte, error) {
	return t.Value, nil
}

func (t *TLV) ParseVisibleString() (string, error) {
	return string(t.Value), nil
}

func (t *TLV) FindChild(class byte, tag uint64) *TLV {
	for _, child := range t.Children {
		if child.Class == class && child.Tag == tag {
			return child
		}
	}
	return nil
}

func (t *TLV) FindChildByTag(tag uint64) *TLV {
	for _, child := range t.Children {
		if child.Tag == tag {
			return child
		}
	}
	return nil
}

func (t *TLV) FindChildrenByTag(tag uint64) []*TLV {
	var result []*TLV
	for _, child := range t.Children {
		if child.Tag == tag {
			result = append(result, child)
		}
	}
	return result
}

type Reader struct {
	data   []byte
	offset int
}

func NewReader(data []byte) *Reader {
	return &Reader{data: data, offset: 0}
}

func (r *Reader) ReadTLV() (*TLV, error) {
	if r.offset >= len(r.data) {
		return nil, io.EOF
	}
	tlv, consumed, err := decodeTLV(r.data, r.offset)
	if err != nil {
		return nil, err
	}
	r.offset += consumed
	return tlv, nil
}

func (r *Reader) Remaining() int {
	if r.offset >= len(r.data) {
		return 0
	}
	return len(r.data) - r.offset
}
