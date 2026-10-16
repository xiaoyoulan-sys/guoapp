package core

import (
	"crypto/md5"
	"encoding/binary"
	"math/bits"
	"strings"
)

// 变种 MD5 与三分支哈希：常量、位序、轮次顺序均不可改动。

func hongguoMedusaIV(iv uint32, data []byte) uint32 {
	value := iv
	for i := 0; i < len(data); i++ {
		if i&1 == 0 {
			value = (value >> 4) ^ value ^ (value << 6) ^ uint32(data[i])
		} else {
			value = ^((value >> 7) ^ value ^ (uint32(data[i]) | (value << 12)))
		}
	}
	return value
}

func hongguoMedusaSumMD5(data []byte) uint32 {
	check := uint32(0x20220420)
	for i := 0; i < 12; i++ {
		var temp uint32
		if i&1 == 0 {
			temp = (check >> 3) ^ check
			check = uint32(data[i]) ^ (check << 7)
		} else {
			temp = (check >> 5) ^ check
			check = ^(uint32(data[i]) | (check << 11))
		}
		check ^= temp
	}
	return (check | 4) ^ 0x1000000
}

func hongguoMD5V3Step(kind int, a, b, c, d, m, shift, constant uint32) uint32 {
	var f uint32
	switch kind {
	case 0:
		f = (b & c) | (^b & d)
	case 1:
		f = (b & d) | (c & ^d)
	case 2:
		f = b ^ c ^ d
	default:
		f = c ^ (b | ^d)
	}
	return b + bits.RotateLeft32(a+f+m+constant, int(shift))
}

func hongguoMD5SumV3(message []byte, countV2 uint32, orders []byte, countV1 uint32) []byte {
	sv := make([]uint32, len(hongguoBranch2SV))
	for i, value := range hongguoBranch2SV {
		sv[i] = bits.RotateLeft32(value, -int(countV1))
	}
	start := [4]uint32{
		bits.RotateLeft32(0x79e0f2fb, -int(countV2)),
		bits.RotateLeft32(0xc8b52570, -int(countV2)),
		bits.RotateLeft32(0xebc2f8cd, -int(countV2)),
		bits.RotateLeft32(0x7c104d93, -int(countV2)),
	}
	endCount := (countV2 + 6) & 255
	end := [4]uint32{
		bits.RotateLeft32(0x19be4866, -int(endCount)),
		bits.RotateLeft32(0xe85986b4, -int(endCount)),
		bits.RotateLeft32(0xe19b326e, -int(endCount)),
		bits.RotateLeft32(0x71d1d7d4, -int(endCount)),
	}
	m := make([]uint32, 16)
	for i := 0; i < 16; i++ {
		m[i] = binary.LittleEndian.Uint32(message[i*4:])
	}
	a, b, c, d := start[0], start[1], start[2], start[3]
	run := func(kind int, shifts [16]int, offset int) {
		for i := 0; i < 16; i++ {
			index := int(orders[offset+i])
			switch i & 3 {
			case 0:
				a = hongguoMD5V3Step(kind, a, b, c, d, m[index], uint32(shifts[i]), sv[offset+i])
			case 1:
				d = hongguoMD5V3Step(kind, d, a, b, c, m[index], uint32(shifts[i]), sv[offset+i])
			case 2:
				c = hongguoMD5V3Step(kind, c, d, a, b, m[index], uint32(shifts[i]), sv[offset+i])
			default:
				b = hongguoMD5V3Step(kind, b, c, d, a, m[index], uint32(shifts[i]), sv[offset+i])
			}
		}
	}
	run(0, [16]int{7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22}, 0)
	run(1, [16]int{5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20}, 16)
	run(2, [16]int{4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23}, 32)
	run(3, [16]int{6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21}, 48)
	ret := make([]byte, 16)
	binary.LittleEndian.PutUint32(ret[0:], (start[0]+a)^end[0])
	binary.LittleEndian.PutUint32(ret[4:], (start[1]+b)^end[1])
	binary.LittleEndian.PutUint32(ret[8:], (start[2]+c)^end[2])
	binary.LittleEndian.PutUint32(ret[12:], (start[3]+d)^end[3])
	tail := make([]byte, 4)
	binary.LittleEndian.PutUint32(tail, hongguoMedusaSumMD5(ret))
	return append(ret, tail...)
}

func hongguoBranch2F13(iv uint32, querySM3, bodyMD5, tsBytes []byte, khronos uint32) []byte {
	v := (iv & 13) * 86
	ivV0 := ((v >> 15) & 255) + ((v >> 8) & 255)
	table := [5]uint32{0x8980f29b, 0xeb549c7f, 0xb08726db, 0xd40cb5e6, 0xe8f559e4}
	n1 := table[ivV0]
	countV1 := (n1 + khronos + 1) & 255
	countV2 := n1 + khronos
	shift := (countV2 + 5) & 7
	seed := [8]byte{0x84, 0x96, 0x77, 0x9d, 0xd4, 0x15, 0x0b, 0xf8}
	pad := make([]byte, 8)
	for i, value := range seed {
		pad[i] = byte((uint32(value) | uint32(value)<<8) >> shift)
	}
	input := make([]byte, 0, len(querySM3)+len(bodyMD5)+len(tsBytes)+12)
	input = append(input, querySM3...)
	input = append(input, bodyMD5...)
	input = append(input, tsBytes...)
	input = append(input, pad...)
	input = append(input, 0xa0, 0x01, 0x00, 0x00)
	orders := hongguoBranch2Orders[(int(ivV0) << 6):((int(ivV0) + 1) << 6)]
	return hongguoMD5SumV3(input, countV2, orders, countV1)
}

func hongguoHashF13(querySM3, bodyMD5, tsBytes []byte, khronos uint32) []byte {
	iv := hongguoMedusaIV(hongguoMedusaIV(hongguoMedusaIV(0x20230928, querySM3), bodyMD5), tsBytes)
	low := iv & 15
	ivV0 := (low * 171) >> 9
	branch := low - ivV0*3
	if branch == 2 {
		return hongguoBranch2F13(iv, querySM3, bodyMD5, tsBytes, khronos)
	}
	if branch != 0 {
		combined := append(append([]byte{}, querySM3...), bodyMD5...)
		tail := make([]byte, 4)
		binary.LittleEndian.PutUint32(tail, hongguoMedusaSumMD5(combined))
		out := append([]byte{}, querySM3[:16]...)
		out = append(out, bodyMD5...)
		return append(out, tail...)
	}
	ivV1List := [6]uint32{0xc4a78580, 0xb3c0fd39, 0xc58c5686, 0xc9aa3ba7, 0xf5a7adf2, 0x963c2ed1}
	ivV1 := ivV1List[ivV0]
	countV1 := (ivV1 + khronos + 1) & 255
	countV2 := ivV1 + khronos
	tt := make([]uint32, len(hongguoMedusaTT))
	for i, value := range hongguoMedusaTT {
		tt[i] = bits.RotateLeft32(value, -int(countV1))
	}
	n0 := (countV2 + 2) & 7
	seed := [4]byte{0xfa, 0x45, 0x61, 0xd7}
	pad := make([]byte, 4)
	for i, value := range seed {
		pad[i] = byte((uint32(value) | uint32(value)<<8) >> n0)
	}
	data := append([]byte{}, querySM3...)
	data = append(data, bodyMD5...)
	data = append(data, tsBytes...)
	data = append(data, pad...)
	data = append(data, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0xa0)
	di := make([]uint32, 0, len(data)/4+112)
	for i := 0; i < len(data); i += 4 {
		di = append(di, binary.BigEndian.Uint32(data[i:]))
	}
	di0 := di[0]
	for i := 0; i < 112; i++ {
		di1, di14 := di[i+1], di[i+14]
		r1 := bits.RotateLeft32(di1, 14) ^ bits.RotateLeft32(di1, 25) ^ (di1 >> 3)
		r2 := bits.RotateLeft32(di14, 13) ^ bits.RotateLeft32(di14, 15) ^ (di14 >> 10)
		di = append(di, di0+di[i+9]+r1+r2)
		di0 = di1
	}
	init := make([]uint32, 8)
	for i, value := range hongguoMedusaInit {
		init[i] = bits.RotateLeft32(value, -int(countV2&31))
	}
	chosen := hongguoMedusaChosen[ivV0]
	d := append([]uint32{}, init...)
	for i := 0; i < chosen[0]; i++ {
		base := ivV1 + uint32(i)
		n0v := di[base&127]
		n1 := ((d[chosen[3]] ^ d[chosen[4]]) & d[chosen[1]]) ^ d[chosen[3]]
		n2 := bits.RotateLeft32(d[chosen[1]], 26) ^ bits.RotateLeft32(d[chosen[1]], 21) ^ bits.RotateLeft32(d[chosen[1]], 7)
		n4 := n0v + n1 + n2 + tt[base&63] + d[chosen[5]]
		n5 := bits.RotateLeft32(d[chosen[2]], 30) ^ bits.RotateLeft32(d[chosen[2]], 19) ^ bits.RotateLeft32(d[chosen[2]], 10)
		n6 := (d[chosen[2]] & d[chosen[6]]) | ((d[chosen[2]] | d[chosen[6]]) & d[chosen[7]])
		n7 := n5 + n6
		old := d[chosen[9]]
		last := d[len(d)-1]
		copy(d[1:], d[:len(d)-1])
		d[0] = last
		d[chosen[10]] = n7 + n4
		d[chosen[8]] = old + n4
	}
	ret := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(ret[i*4:], d[i]+init[i])
	}
	folded := make([]byte, 16)
	for i := 0; i < 16; i++ {
		folded[i] = ret[i] ^ ret[16+i]
	}
	copy(ret, folded)
	out := append([]byte{}, ret[:16]...)
	tail := make([]byte, 4)
	binary.LittleEndian.PutUint32(tail, hongguoMedusaSumMD5(ret[:16]))
	return append(out, tail...)
}

func hongguoBranchOf(rawURL string, body []byte, khronos uint32) uint32 {
	bodyMD5 := make([]byte, 16)
	if body != nil {
		sum := md5.Sum(body)
		copy(bodyMD5, sum[:])
	}
	query := rawURL
	if index := strings.IndexByte(rawURL, '?'); index >= 0 {
		query = rawURL[index+1:]
	}
	querySM3 := hongguoSM3([]byte(query))
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, khronos)
	iv := hongguoMedusaIV(hongguoMedusaIV(hongguoMedusaIV(0x20230928, querySM3[:]), bodyMD5), ts)
	low := iv & 15
	return low - ((low*171)>>9)*3
}
