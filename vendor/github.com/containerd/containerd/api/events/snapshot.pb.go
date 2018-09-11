// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: github.com/containerd/containerd/api/events/snapshot.proto

package events

import proto "github.com/gogo/protobuf/proto"
import fmt "fmt"
import math "math"

// skipping weak import containerd_plugin "github.com/containerd/containerd/protobuf/plugin"

import strings "strings"
import reflect "reflect"

import io "io"

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

type SnapshotPrepare struct {
	Key    string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Parent string `protobuf:"bytes,2,opt,name=parent,proto3" json:"parent,omitempty"`
}

func (m *SnapshotPrepare) Reset()                    { *m = SnapshotPrepare{} }
func (*SnapshotPrepare) ProtoMessage()               {}
func (*SnapshotPrepare) Descriptor() ([]byte, []int) { return fileDescriptorSnapshot, []int{0} }

type SnapshotCommit struct {
	Key  string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Name string `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
}

func (m *SnapshotCommit) Reset()                    { *m = SnapshotCommit{} }
func (*SnapshotCommit) ProtoMessage()               {}
func (*SnapshotCommit) Descriptor() ([]byte, []int) { return fileDescriptorSnapshot, []int{1} }

type SnapshotRemove struct {
	Key string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
}

func (m *SnapshotRemove) Reset()                    { *m = SnapshotRemove{} }
func (*SnapshotRemove) ProtoMessage()               {}
func (*SnapshotRemove) Descriptor() ([]byte, []int) { return fileDescriptorSnapshot, []int{2} }

func init() {
	proto.RegisterType((*SnapshotPrepare)(nil), "containerd.events.SnapshotPrepare")
	proto.RegisterType((*SnapshotCommit)(nil), "containerd.events.SnapshotCommit")
	proto.RegisterType((*SnapshotRemove)(nil), "containerd.events.SnapshotRemove")
}

// Field returns the value for the given fieldpath as a string, if defined.
// If the value is not defined, the second value will be false.
func (m *SnapshotPrepare) Field(fieldpath []string) (string, bool) {
	if len(fieldpath) == 0 {
		return "", false
	}

	switch fieldpath[0] {
	case "key":
		return string(m.Key), len(m.Key) > 0
	case "parent":
		return string(m.Parent), len(m.Parent) > 0
	}
	return "", false
}

// Field returns the value for the given fieldpath as a string, if defined.
// If the value is not defined, the second value will be false.
func (m *SnapshotCommit) Field(fieldpath []string) (string, bool) {
	if len(fieldpath) == 0 {
		return "", false
	}

	switch fieldpath[0] {
	case "key":
		return string(m.Key), len(m.Key) > 0
	case "name":
		return string(m.Name), len(m.Name) > 0
	}
	return "", false
}

// Field returns the value for the given fieldpath as a string, if defined.
// If the value is not defined, the second value will be false.
func (m *SnapshotRemove) Field(fieldpath []string) (string, bool) {
	if len(fieldpath) == 0 {
		return "", false
	}

	switch fieldpath[0] {
	case "key":
		return string(m.Key), len(m.Key) > 0
	}
	return "", false
}
func (m *SnapshotPrepare) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *SnapshotPrepare) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	if len(m.Key) > 0 {
		dAtA[i] = 0xa
		i++
		i = encodeVarintSnapshot(dAtA, i, uint64(len(m.Key)))
		i += copy(dAtA[i:], m.Key)
	}
	if len(m.Parent) > 0 {
		dAtA[i] = 0x12
		i++
		i = encodeVarintSnapshot(dAtA, i, uint64(len(m.Parent)))
		i += copy(dAtA[i:], m.Parent)
	}
	return i, nil
}

func (m *SnapshotCommit) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *SnapshotCommit) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	if len(m.Key) > 0 {
		dAtA[i] = 0xa
		i++
		i = encodeVarintSnapshot(dAtA, i, uint64(len(m.Key)))
		i += copy(dAtA[i:], m.Key)
	}
	if len(m.Name) > 0 {
		dAtA[i] = 0x12
		i++
		i = encodeVarintSnapshot(dAtA, i, uint64(len(m.Name)))
		i += copy(dAtA[i:], m.Name)
	}
	return i, nil
}

func (m *SnapshotRemove) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *SnapshotRemove) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	if len(m.Key) > 0 {
		dAtA[i] = 0xa
		i++
		i = encodeVarintSnapshot(dAtA, i, uint64(len(m.Key)))
		i += copy(dAtA[i:], m.Key)
	}
	return i, nil
}

func encodeVarintSnapshot(dAtA []byte, offset int, v uint64) int {
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return offset + 1
}
func (m *SnapshotPrepare) Size() (n int) {
	var l int
	_ = l
	l = len(m.Key)
	if l > 0 {
		n += 1 + l + sovSnapshot(uint64(l))
	}
	l = len(m.Parent)
	if l > 0 {
		n += 1 + l + sovSnapshot(uint64(l))
	}
	return n
}

func (m *SnapshotCommit) Size() (n int) {
	var l int
	_ = l
	l = len(m.Key)
	if l > 0 {
		n += 1 + l + sovSnapshot(uint64(l))
	}
	l = len(m.Name)
	if l > 0 {
		n += 1 + l + sovSnapshot(uint64(l))
	}
	return n
}

func (m *SnapshotRemove) Size() (n int) {
	var l int
	_ = l
	l = len(m.Key)
	if l > 0 {
		n += 1 + l + sovSnapshot(uint64(l))
	}
	return n
}

func sovSnapshot(x uint64) (n int) {
	for {
		n++
		x >>= 7
		if x == 0 {
			break
		}
	}
	return n
}
func sozSnapshot(x uint64) (n int) {
	return sovSnapshot(uint64((x << 1) ^ uint64((int64(x) >> 63))))
}
func (this *SnapshotPrepare) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&SnapshotPrepare{`,
		`Key:` + fmt.Sprintf("%v", this.Key) + `,`,
		`Parent:` + fmt.Sprintf("%v", this.Parent) + `,`,
		`}`,
	}, "")
	return s
}
func (this *SnapshotCommit) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&SnapshotCommit{`,
		`Key:` + fmt.Sprintf("%v", this.Key) + `,`,
		`Name:` + fmt.Sprintf("%v", this.Name) + `,`,
		`}`,
	}, "")
	return s
}
func (this *SnapshotRemove) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&SnapshotRemove{`,
		`Key:` + fmt.Sprintf("%v", this.Key) + `,`,
		`}`,
	}, "")
	return s
}
func valueToStringSnapshot(v interface{}) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("*%v", pv)
}
func (m *SnapshotPrepare) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowSnapshot
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: SnapshotPrepare: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: SnapshotPrepare: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Key", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= (uint64(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthSnapshot
			}
			postIndex := iNdEx + intStringLen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Key = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Parent", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= (uint64(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthSnapshot
			}
			postIndex := iNdEx + intStringLen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Parent = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipSnapshot(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthSnapshot
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (m *SnapshotCommit) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowSnapshot
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: SnapshotCommit: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: SnapshotCommit: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Key", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= (uint64(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthSnapshot
			}
			postIndex := iNdEx + intStringLen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Key = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Name", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= (uint64(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthSnapshot
			}
			postIndex := iNdEx + intStringLen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Name = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipSnapshot(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthSnapshot
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (m *SnapshotRemove) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowSnapshot
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: SnapshotRemove: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: SnapshotRemove: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Key", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= (uint64(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthSnapshot
			}
			postIndex := iNdEx + intStringLen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Key = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipSnapshot(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthSnapshot
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func skipSnapshot(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowSnapshot
			}
			if iNdEx >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
			return iNdEx, nil
		case 1:
			iNdEx += 8
			return iNdEx, nil
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowSnapshot
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			iNdEx += length
			if length < 0 {
				return 0, ErrInvalidLengthSnapshot
			}
			return iNdEx, nil
		case 3:
			for {
				var innerWire uint64
				var start int = iNdEx
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return 0, ErrIntOverflowSnapshot
					}
					if iNdEx >= l {
						return 0, io.ErrUnexpectedEOF
					}
					b := dAtA[iNdEx]
					iNdEx++
					innerWire |= (uint64(b) & 0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				innerWireType := int(innerWire & 0x7)
				if innerWireType == 4 {
					break
				}
				next, err := skipSnapshot(dAtA[start:])
				if err != nil {
					return 0, err
				}
				iNdEx = start + next
			}
			return iNdEx, nil
		case 4:
			return iNdEx, nil
		case 5:
			iNdEx += 4
			return iNdEx, nil
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
	}
	panic("unreachable")
}

var (
	ErrInvalidLengthSnapshot = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowSnapshot   = fmt.Errorf("proto: integer overflow")
)

func init() {
	proto.RegisterFile("github.com/containerd/containerd/api/events/snapshot.proto", fileDescriptorSnapshot)
}

var fileDescriptorSnapshot = []byte{
	// 235 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0xb2, 0x4a, 0xcf, 0x2c, 0xc9,
	0x28, 0x4d, 0xd2, 0x4b, 0xce, 0xcf, 0xd5, 0x4f, 0xce, 0xcf, 0x2b, 0x49, 0xcc, 0xcc, 0x4b, 0x2d,
	0x4a, 0x41, 0x66, 0x26, 0x16, 0x64, 0xea, 0xa7, 0x96, 0xa5, 0xe6, 0x95, 0x14, 0xeb, 0x17, 0xe7,
	0x25, 0x16, 0x14, 0x67, 0xe4, 0x97, 0xe8, 0x15, 0x14, 0xe5, 0x97, 0xe4, 0x0b, 0x09, 0x22, 0x54,
	0xe9, 0x41, 0x54, 0x48, 0x39, 0x10, 0x34, 0x0e, 0xac, 0x35, 0xa9, 0x34, 0x4d, 0xbf, 0x20, 0xa7,
	0x34, 0x3d, 0x33, 0x4f, 0x3f, 0x2d, 0x33, 0x35, 0x27, 0xa5, 0x20, 0xb1, 0x24, 0x03, 0x62, 0xa8,
	0x92, 0x35, 0x17, 0x7f, 0x30, 0xd4, 0x9a, 0x80, 0xa2, 0xd4, 0x82, 0xc4, 0xa2, 0x54, 0x21, 0x01,
	0x2e, 0xe6, 0xec, 0xd4, 0x4a, 0x09, 0x46, 0x05, 0x46, 0x0d, 0xce, 0x20, 0x10, 0x53, 0x48, 0x8c,
	0x8b, 0x0d, 0x24, 0x93, 0x57, 0x22, 0xc1, 0x04, 0x16, 0x84, 0xf2, 0x94, 0xcc, 0xb8, 0xf8, 0x60,
	0x9a, 0x9d, 0xf3, 0x73, 0x73, 0x33, 0x4b, 0xb0, 0xe8, 0x15, 0xe2, 0x62, 0xc9, 0x4b, 0xcc, 0x4d,
	0x85, 0xea, 0x04, 0xb3, 0x95, 0x94, 0x10, 0xfa, 0x82, 0x52, 0x73, 0xf3, 0xcb, 0xb0, 0xd8, 0xe9,
	0x14, 0x70, 0xe2, 0xa1, 0x1c, 0xc3, 0x8d, 0x87, 0x72, 0x0c, 0x0d, 0x8f, 0xe4, 0x18, 0x4f, 0x3c,
	0x92, 0x63, 0xbc, 0xf0, 0x48, 0x8e, 0xf1, 0xc1, 0x23, 0x39, 0xc6, 0x05, 0x5f, 0xe4, 0x18, 0xa3,
	0x8c, 0x48, 0x08, 0x47, 0x6b, 0x08, 0x15, 0xc1, 0x90, 0xc4, 0x06, 0xf6, 0xb3, 0x31, 0x20, 0x00,
	0x00, 0xff, 0xff, 0x69, 0x66, 0xa9, 0x2a, 0x86, 0x01, 0x00, 0x00,
}
