package core

import (
	"encoding/binary"
	"math"
)

func hongguoProtoVarint(value uint64) []byte {
	var out []byte
	for value > 127 {
		out = append(out, byte(value&127|128))
		value >>= 7
	}
	return append(out, byte(value))
}

func hongguoProtoZigzag(value int64) uint64 {
	if value < 0 {
		return uint64(-value)*2 - 1
	}
	return uint64(value) * 2
}

func hongguoProtoAppendSint(dst []byte, tag int, value int64) []byte {
	dst = append(dst, hongguoProtoVarint(uint64(tag)<<3)...)
	return append(dst, hongguoProtoVarint(hongguoProtoZigzag(value))...)
}

func hongguoProtoAppendBytes(dst []byte, tag int, value []byte) []byte {
	dst = append(dst, hongguoProtoVarint(uint64(tag)<<3|2)...)
	dst = append(dst, hongguoProtoVarint(uint64(len(value)))...)
	return append(dst, value...)
}

func hongguoProtoAppendString(dst []byte, tag int, value string) []byte {
	return hongguoProtoAppendBytes(dst, tag, []byte(value))
}

func hongguoProtoAppendFloat(dst []byte, tag int, value float32) []byte {
	dst = append(dst, hongguoProtoVarint(uint64(tag)<<3|5)...)
	var buffer [4]byte
	binary.LittleEndian.PutUint32(buffer[:], math.Float32bits(value))
	return append(dst, buffer[:]...)
}

func hongguoDeviceProto(deviceID, version string) []byte {
	var out []byte
	out = hongguoProtoAppendSint(out, 1, 1)
	out = hongguoProtoAppendSint(out, 2, 2)
	out = hongguoProtoAppendString(out, 3, "8662")
	out = hongguoProtoAppendString(out, 4, deviceID)
	out = hongguoProtoAppendString(out, 5, "Ai6svO3PyrwDOUSmO6ZcResxu")
	out = hongguoProtoAppendString(out, 6, "!noperm!")
	out = hongguoProtoAppendSint(out, 7, -888888)
	out = hongguoProtoAppendSint(out, 8, -888888)
	out = hongguoProtoAppendSint(out, 9, 3)
	out = hongguoProtoAppendSint(out, 10, -888888)
	out = hongguoProtoAppendString(out, 11, "!notset!")
	out = hongguoProtoAppendString(out, 12, "Asia/Shanghai,8")
	out = hongguoProtoAppendString(out, 13, "zh_CN")
	out = hongguoProtoAppendSint(out, 14, 4)
	out = hongguoProtoAppendFloat(out, 16, 255.24993896484375)
	out = hongguoProtoAppendFloat(out, 17, 35.58599090576172)
	out = hongguoProtoAppendFloat(out, 18, 3.467449188232422)
	out = hongguoProtoAppendFloat(out, 19, 3.467449188232422)
	out = hongguoProtoAppendFloat(out, 20, 255.1754913330078)
	out = hongguoProtoAppendFloat(out, 21, 42.17544174194336)
	out = hongguoProtoAppendString(out, 22, "16")
	out = hongguoProtoAppendSint(out, 23, 41)
	out = hongguoProtoAppendSint(out, 24, 36)
	out = hongguoProtoAppendSint(out, 25, 1728388016635)
	out = hongguoProtoAppendSint(out, 26, 1728388016635)
	out = hongguoProtoAppendSint(out, 27, 1728388016635)
	out = hongguoProtoAppendSint(out, 28, 1728388016637)
	out = hongguoProtoAppendSint(out, 29, -1)
	out = hongguoProtoAppendString(out, 30, "25053RT47C")
	out = hongguoProtoAppendString(out, 31, "Redmi")
	out = hongguoProtoAppendString(out, 32, "25053RT47C")
	out = hongguoProtoAppendString(out, 33, "25053RT47C")
	out = hongguoProtoAppendString(out, 34, "Xiaomi")
	out = hongguoProtoAppendString(out, 35, "Redmi")
	out = hongguoProtoAppendString(out, 36, "Redmi")
	out = hongguoProtoAppendSint(out, 38, 31)
	return out
}
