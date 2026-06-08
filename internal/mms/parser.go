package mms

import (
	"fmt"
	"time"

	"github.com/vpp/dispatch-engine/internal/ber"
)

const (
	PDUConfirmedRequest    = 0xA0
	PDUConfirmedResponse   = 0xA1
	PDUUnconfirmedRequest  = 0xA2
	PDUUnconfirmedResponse = 0xA3
	PDUReject              = 0xA4
	PDUCancelRequest       = 0xA5
	PDUCancelResponse      = 0xA6

	ServiceRead        = 0xA4
	ServiceWrite       = 0xA5
	ServiceInformationReport = 0xA0
	ServiceDefineNamedVariableList = 0xA9
	ServiceGetNamedVariableListAttributes = 0xAB

	DataTypeStructure  = 0xA2
	DataTypeArray      = 0xA1
	DataTypeBoolean    = 0x83
	DataTypeBitString  = 0x84
	DataTypeInteger    = 0x85
	DataTypeUnsigned   = 0x86
	DataTypeFloat      = 0x87
	DataTypeVisibleString = 0x8A
	DataTypeOctetString   = 0x89
	DataTypeBinaryTime    = 0x8C
	DataTypeUTCTime       = 0x91
)

type NodeMeasurement struct {
	NodeID      string
	Timestamp   time.Time
	ActivePower float64
	ReactivePower float64
	SOC         float64
	Voltage     float64
	Current     float64
	Frequency   float64
	RawData     []byte
}

type SubstationData struct {
	IEDName     string
	DomainID    string
	ItemID      string
	Measurements map[string]*NodeMeasurement
	ReceivedAt   time.Time
}

func ParseMMSPDU(data []byte) (*SubstationData, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("mms: packet too short (%d bytes)", len(data))
	}

	tlv, _, err := ber.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("mms: BER decode failed: %w", err)
	}

	result := &SubstationData{
		Measurements: make(map[string]*NodeMeasurement),
		ReceivedAt:   time.Now(),
	}

	switch tlv.Class | byte(tlv.Tag&0x1F) {
	case PDUConfirmedRequest, PDUUnconfirmedRequest:
		err = parseRequest(tlv, result)
	case PDUConfirmedResponse, PDUUnconfirmedResponse:
		err = parseResponse(tlv, result)
	default:
		err = parseGenericPDU(tlv, result)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

func parseRequest(tlv *ber.TLV, result *SubstationData) error {
	service := tlv.FindChildByTag(2)
	if service == nil {
		return extractMeasurements(tlv, result)
	}

	switch service.TagCode() {
	case ServiceInformationReport:
		return parseInformationReport(service, result)
	case ServiceRead:
		return parseReadResponse(service, result)
	default:
		return extractMeasurements(tlv, result)
	}
}

func parseResponse(tlv *ber.TLV, result *SubstationData) error {
	return extractMeasurements(tlv, result)
}

func parseGenericPDU(tlv *ber.TLV, result *SubstationData) error {
	return extractMeasurements(tlv, result)
}

func parseInformationReport(tlv *ber.TLV, result *SubstationData) error {
	varName := tlv.FindChildByTag(0)
	if varName != nil {
		nameStr, _ := varName.ParseVisibleString()
		result.DomainID = nameStr
	}

	accessResult := tlv.FindChildByTag(4)
	if accessResult == nil {
		accessResult = tlv.FindChildByTag(3)
	}
	if accessResult == nil {
		return nil
	}

	return extractDataItems(accessResult, result)
}

func parseReadResponse(tlv *ber.TLV, result *SubstationData) error {
	return extractDataItems(tlv, result)
}

func extractMeasurements(tlv *ber.TLV, result *SubstationData) error {
	return extractDataItems(tlv, result)
}

func extractDataItems(tlv *ber.TLV, result *SubstationData) error {
	meas := &NodeMeasurement{
		Timestamp: time.Now(),
	}

	err := walkTLVForMeasurements(tlv, meas)
	if err != nil {
		return err
	}

	if meas.ActivePower != 0 || meas.ReactivePower != 0 || meas.SOC != 0 {
		if meas.NodeID == "" {
			meas.NodeID = fmt.Sprintf("node_%d", len(result.Measurements))
		}
		result.Measurements[meas.NodeID] = meas
	}

	for _, child := range tlv.Children {
		if child.IsConstructed() {
			childMeas := &NodeMeasurement{
				Timestamp: time.Now(),
			}
			err := walkTLVForMeasurements(child, childMeas)
			if err == nil && (childMeas.ActivePower != 0 || childMeas.ReactivePower != 0 || childMeas.SOC != 0) {
				if childMeas.NodeID == "" {
					childMeas.NodeID = fmt.Sprintf("node_%d", len(result.Measurements))
				}
				result.Measurements[childMeas.NodeID] = childMeas
			}
		}
	}

	return nil
}

func walkTLVForMeasurements(tlv *ber.TLV, meas *NodeMeasurement) error {
	for _, child := range tlv.Children {
		if child.IsConstructed() {
			if err := walkTLVForMeasurements(child, meas); err != nil {
				return err
			}
			continue
		}

		switch child.Tag {
		case DataTypeFloat:
			val, err := child.ParseMMSFloat()
			if err != nil {
				continue
			}
			classifyValue(meas, val)

		case DataTypeInteger:
			val, err := child.ParseInteger()
			if err != nil {
				continue
			}
			classifyValue(meas, float64(val))

		case DataTypeUnsigned:
			val, err := child.ParseUnsigned()
			if err != nil {
				continue
			}
			classifyValue(meas, float64(val))

		case DataTypeVisibleString:
			val, err := child.ParseVisibleString()
			if err != nil {
				continue
			}
			if meas.NodeID == "" {
				meas.NodeID = val
			}

		case DataTypeBitString:
			_, _, _ = child.ParseBitString()

		case DataTypeBoolean:
			_, _ = child.ParseBoolean()
		}
	}
	return nil
}

func classifyValue(meas *NodeMeasurement, val float64) {
	switch {
	case meas.ActivePower == 0 && val >= -1000 && val <= 1000:
		meas.ActivePower = val
	case meas.ReactivePower == 0 && val >= -1000 && val <= 1000:
		meas.ReactivePower = val
	case meas.SOC == 0 && val >= 0 && val <= 100:
		meas.SOC = val
	case meas.Voltage == 0 && val >= 0 && val <= 500:
		meas.Voltage = val
	case meas.Current == 0 && val >= -5000 && val <= 5000:
		meas.Current = val
	case meas.Frequency == 0 && val >= 49 && val <= 51:
		meas.Frequency = val
	}
}

type COTP struct {
	Length    byte
	PDUType   byte
	DstRef    uint16
	SrcRef    uint16
	ClassOpt  byte
}

type TPKT struct {
	Version  byte
	Reserved byte
	Length   uint16
}

func ParseTPKT(data []byte) (*TPKT, []byte, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("tpkt: header too short")
	}

	tpkt := &TPKT{
		Version:  data[0],
		Reserved: data[1],
		Length:   uint16(data[2])<<8 | uint16(data[3]),
	}

	if tpkt.Version != 3 {
		return nil, nil, fmt.Errorf("tpkt: invalid version %d", tpkt.Version)
	}

	if int(tpkt.Length) > len(data) {
		return nil, nil, fmt.Errorf("tpkt: packet length %d exceeds buffer %d", tpkt.Length, len(data))
	}

	payload := data[4:tpkt.Length]
	return tpkt, payload, nil
}

func ParseCOTP(data []byte) (*COTP, []byte, error) {
	if len(data) < 3 {
		return nil, nil, fmt.Errorf("cotp: header too short")
	}

	cotp := &COTP{
		Length:  data[0],
		PDUType: data[1],
	}

	headerEnd := int(cotp.Length) + 1
	if headerEnd > len(data) {
		headerEnd = len(data)
	}

	if cotp.Length >= 2 && len(data) >= 6 {
		cotp.DstRef = uint16(data[2])<<8 | uint16(data[3])
		cotp.SrcRef = uint16(data[4])<<8 | uint16(data[5])
		if cotp.Length >= 6 && len(data) > 6 {
			cotp.ClassOpt = data[6]
		}
	}

	payload := data[headerEnd:]
	return cotp, payload, nil
}
