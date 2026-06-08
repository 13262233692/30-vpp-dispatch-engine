package ber

import (
	"testing"
)

func TestDecodeSimpleInteger(t *testing.T) {
	data := []byte{0x02, 0x01, 0x2A}

	tlv, consumed, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if consumed != 3 {
		t.Errorf("expected consumed=3, got %d", consumed)
	}
	if tlv.Tag != TagInteger {
		t.Errorf("expected tag=%d, got %d", TagInteger, tlv.Tag)
	}
	if tlv.Constructed {
		t.Error("should not be constructed")
	}

	val, err := tlv.ParseInteger()
	if err != nil {
		t.Fatalf("parse integer failed: %v", err)
	}
	if val != 42 {
		t.Errorf("expected 42, got %d", val)
	}
}

func TestDecodeNegativeInteger(t *testing.T) {
	data := []byte{0x02, 0x01, 0xFF}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	val, err := tlv.ParseInteger()
	if err != nil {
		t.Fatalf("parse integer failed: %v", err)
	}
	if val != -1 {
		t.Errorf("expected -1, got %d", val)
	}
}

func TestDecodeLongFormLength(t *testing.T) {
	value := make([]byte, 200)
	for i := range value {
		value[i] = byte(i)
	}

	data := []byte{0x04, 0x81, 0xC8}
	data = append(data, value...)

	tlv, consumed, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if tlv.Length != 200 {
		t.Errorf("expected length=200, got %d", tlv.Length)
	}
	if consumed != 203 {
		t.Errorf("expected consumed=203, got %d", consumed)
	}
}

func TestDecodeSequence(t *testing.T) {
	data := []byte{
		0x30, 0x06,
		0x02, 0x01, 0x01,
		0x02, 0x01, 0x02,
	}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !tlv.Constructed {
		t.Error("sequence should be constructed")
	}
	if len(tlv.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(tlv.Children))
	}

	val1, _ := tlv.Children[0].ParseInteger()
	val2, _ := tlv.Children[1].ParseInteger()
	if val1 != 1 || val2 != 2 {
		t.Errorf("expected children [1,2], got [%d,%d]", val1, val2)
	}
}

func TestDecodeContextSpecific(t *testing.T) {
	data := []byte{0xA0, 0x03, 0x02, 0x01, 0x05}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if tlv.Class != ClassContextSpecific {
		t.Errorf("expected context-specific class, got %d", tlv.Class)
	}
	if !tlv.Constructed {
		t.Error("should be constructed")
	}
	if tlv.Tag != 0 {
		t.Errorf("expected tag=0, got %d", tlv.Tag)
	}
}

func TestDecodeMultipleTLV(t *testing.T) {
	data := []byte{
		0x02, 0x01, 0x0A,
		0x02, 0x01, 0x14,
	}

	tlvs, err := DecodeAll(data)
	if err != nil {
		t.Fatalf("decode all failed: %v", err)
	}
	if len(tlvs) != 2 {
		t.Fatalf("expected 2 TLVs, got %d", len(tlvs))
	}

	v1, _ := tlvs[0].ParseInteger()
	v2, _ := tlvs[1].ParseInteger()
	if v1 != 10 || v2 != 20 {
		t.Errorf("expected [10,20], got [%d,%d]", v1, v2)
	}
}

func TestDecodeHighTagNumber(t *testing.T) {
	data := []byte{0x9F, 0x20, 0x01, 0x05}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if tlv.Tag != 0x20 {
		t.Errorf("expected tag=0x20, got 0x%X", tlv.Tag)
	}
}

func TestParseBoolean(t *testing.T) {
	data := []byte{0x01, 0x01, 0xFF}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	val, err := tlv.ParseBoolean()
	if err != nil {
		t.Fatalf("parse boolean failed: %v", err)
	}
	if !val {
		t.Error("expected true")
	}
}

func TestParseVisibleString(t *testing.T) {
	data := []byte{0x1A, 0x05, 'H', 'E', 'L', 'L', 'O'}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	val, err := tlv.ParseVisibleString()
	if err != nil {
		t.Fatalf("parse visible string failed: %v", err)
	}
	if val != "HELLO" {
		t.Errorf("expected HELLO, got %s", val)
	}
}

func TestParseFloat32(t *testing.T) {
	data := []byte{0x04, 0x04, 0x42, 0x28, 0x00, 0x00}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	val, err := tlv.ParseFloat32()
	if err != nil {
		t.Fatalf("parse float32 failed: %v", err)
	}

	expected := float32(42.0)
	if val != expected {
		t.Errorf("expected %f, got %f", expected, val)
	}
}

func TestFindChild(t *testing.T) {
	data := []byte{
		0x30, 0x05,
		0x02, 0x01, 0x01,
		0x05, 0x00,
	}

	tlv, _, err := Decode(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	child := tlv.FindChild(ClassUniversal, TagInteger)
	if child == nil {
		t.Fatal("expected to find integer child")
	}

	val, _ := child.ParseInteger()
	if val != 1 {
		t.Errorf("expected 1, got %d", val)
	}

	nullChild := tlv.FindChild(ClassUniversal, TagNull)
	if nullChild == nil {
		t.Fatal("expected to find null child")
	}
}

func TestTruncatedData(t *testing.T) {
	data := []byte{0x02, 0x02, 0x01}

	_, _, err := Decode(data)
	if err == nil {
		t.Error("expected truncated error")
	}
}

func TestReader(t *testing.T) {
	data := []byte{
		0x02, 0x01, 0x01,
		0x02, 0x01, 0x02,
	}

	reader := NewReader(data)

	tlv1, err := reader.ReadTLV()
	if err != nil {
		t.Fatalf("read TLV 1 failed: %v", err)
	}
	v1, _ := tlv1.ParseInteger()
	if v1 != 1 {
		t.Errorf("expected 1, got %d", v1)
	}

	tlv2, err := reader.ReadTLV()
	if err != nil {
		t.Fatalf("read TLV 2 failed: %v", err)
	}
	v2, _ := tlv2.ParseInteger()
	if v2 != 2 {
		t.Errorf("expected 2, got %d", v2)
	}

	if reader.Remaining() != 0 {
		t.Errorf("expected 0 remaining, got %d", reader.Remaining())
	}
}
