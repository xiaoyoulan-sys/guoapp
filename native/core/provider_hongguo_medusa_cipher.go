package core

import "encoding/binary"

// 红果自研 AES 变体：S 盒按 khronos 低两位分四段，密钥扩展与列混合顺序不可改动。

type hongguoAesV3 struct {
	box   []byte
	con2  [4]int
	order [16]int
	keys  [12][4]byte
}

func hongguoAesMatrix(value []byte) [4][4]byte {
	var rows [4][4]byte
	for i := 0; i < 4; i++ {
		copy(rows[i][:], value[i*4:i*4+4])
	}
	return rows
}

func hongguoAesFlat(rows *[4][4]byte) []byte {
	out := make([]byte, 0, 16)
	for i := 0; i < 4; i++ {
		out = append(out, rows[i][:]...)
	}
	return out
}

func hongguoAesMix(rows *[4][4]byte) {
	xt := func(x byte) byte {
		if x&128 != 0 {
			return (x << 1) ^ 0x1b
		}
		return x << 1
	}
	for i := 0; i < 4; i++ {
		t := rows[0][i] ^ rows[1][i] ^ rows[2][i] ^ rows[3][i]
		u := rows[0][i]
		rows[0][i] ^= t ^ xt(rows[0][i]^rows[1][i])
		rows[1][i] ^= t ^ xt(rows[1][i]^rows[2][i])
		rows[2][i] ^= t ^ xt(rows[2][i]^rows[3][i])
		rows[3][i] ^= t ^ xt(rows[3][i]^u)
	}
}

func hongguoXorBytes(a, b []byte) []byte {
	out := append([]byte{}, a...)
	for i := range out {
		out[i] ^= b[i%len(b)]
	}
	return out
}

func newHongguoAesV3(key []byte, khronos uint32) *hongguoAesV3 {
	wordSize := int(khronos & 3)
	con2Table := [4][4]int{{1, 0, 2, 3}, {2, 0, 3, 1}, {0, 1, 3, 2}, {1, 0, 2, 3}}
	orderTable := [4][16]int{
		{0, 9, 14, 11, 4, 13, 2, 7, 8, 1, 6, 15, 12, 5, 10, 3},
		{0, 9, 14, 15, 4, 13, 2, 7, 8, 1, 6, 3, 12, 5, 10, 11},
		{0, 9, 14, 7, 4, 13, 2, 11, 8, 1, 6, 3, 12, 5, 10, 15},
		{0, 9, 14, 11, 4, 13, 2, 7, 8, 1, 6, 15, 12, 5, 10, 3},
	}
	cipher := &hongguoAesV3{
		box:   hongguoSBox[wordSize*256 : (wordSize+1)*256],
		con2:  con2Table[wordSize],
		order: orderTable[wordSize],
	}
	initTable := [4]uint32{0xca025ddc, 0x823dc546, 0xc9420583, 0xc298225f}
	init := initTable[wordSize]
	initial := make([]byte, 16)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint32(initial[i*4:], init)
	}
	expanded := make([]byte, 48)
	for i := 0; i < 16; i++ {
		expanded[i] = initial[i] ^ key[i]
	}
	rounds := 8
	for i := 4; i < 12; i++ {
		at := 4 * (i - 1)
		a, b, c, d := expanded[at], expanded[at+1], expanded[at+2], expanded[at+3]
		if i&3 == 0 {
			t := byte(uint32(int32(init)>>uint(rounds&24)) ^ uint32(cipher.box[b]))
			b, c, d, a = cipher.box[c], cipher.box[d], cipher.box[a], t
		}
		rounds += 2
		expanded[at+4] = a ^ expanded[at-12]
		expanded[at+5] = b ^ expanded[at-11]
		expanded[at+6] = c ^ expanded[at-10]
		expanded[at+7] = d ^ expanded[at-9]
	}
	for i := 0; i < 12; i++ {
		copy(cipher.keys[i][:], expanded[i*4:i*4+4])
	}
	return cipher
}

func (cipher *hongguoAesV3) add(rows *[4][4]byte, keys [][4]byte) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			rows[i][j] ^= keys[i][j]
		}
	}
}

func (cipher *hongguoAesV3) addCon(rows *[4][4]byte, keys [][4]byte) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			rows[i][j] ^= keys[i][cipher.con2[j]]
		}
	}
}

func (cipher *hongguoAesV3) sub(rows *[4][4]byte) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			rows[i][j] = cipher.box[rows[i][j]]
		}
	}
	old := *rows
	for i := 0; i < 4; i++ {
		rows[i] = old[cipher.con2[i]]
	}
}

func (cipher *hongguoAesV3) shift(rows *[4][4]byte) {
	flat := hongguoAesFlat(rows)
	for i := 0; i < 16; i++ {
		rows[i/4][i%4] = flat[cipher.order[i]]
	}
}

func (cipher *hongguoAesV3) shiftCon(rows *[4][4]byte) {
	old := *rows
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			rows[i][j] = old[i][cipher.con2[j]]
		}
	}
}

func (cipher *hongguoAesV3) block(value []byte) []byte {
	rows := hongguoAesMatrix(value)
	cipher.addCon(&rows, cipher.keys[0:4])
	for i := 1; i < 3; i++ {
		cipher.sub(&rows)
		cipher.shift(&rows)
		if i == 1 {
			cipher.shiftCon(&rows)
			hongguoAesMix(&rows)
		}
		cipher.addCon(&rows, cipher.keys[i*4:])
	}
	cipher.add(&rows, cipher.keys[4:])
	return hongguoAesFlat(&rows)
}

func (cipher *hongguoAesV3) encrypt(data, iv []byte) []byte {
	source := append([]byte{}, data...)
	plaintext := make([]byte, 32)
	for i := 0; i < 31; i++ {
		at := i * 8
		n0 := (source[at] >> 4) & 2
		n1 := n0 | (source[at+1] & 64)
		n2 := n1 | ((source[at+2] >> 2) & 1)
		n3 := n2 | ((source[at+3] << 3) & 128)
		n4 := n3 | ((source[at+4] >> 1) & 4)
		n5 := n4 | ((source[at+5] << 3) & 16)
		n6 := n5 | ((source[at+6] << 5) & 32)
		plaintext[i] = n6 | ((source[at+7] >> 4) & 8)
	}
	plaintext[31] = 1
	key := make([]byte, 0, 32)
	previous := append([]byte{}, iv...)
	for i := 0; i < len(plaintext); i += 16 {
		value := cipher.block(hongguoXorBytes(plaintext[i:i+16], previous))
		key = append(key, value...)
		previous = value
	}
	out := append([]byte{}, source...)
	for i := 0; i < 31; i++ {
		at := i * 8
		k := key[i]
		out[at] &= 0xdf
		out[at] |= (k << 4) & 32
		out[at+1] &= 0xbf
		out[at+1] |= k & 64
		out[at+2] &= 0xfb
		out[at+2] |= (k << 2) & 4
		out[at+3] &= 0xef
		out[at+3] |= (k >> 3) & 16
		out[at+4] &= 0xf7
		out[at+4] |= (k + k) & 8
		out[at+5] &= 0xfd
		out[at+5] |= (k >> 3) & 2
		out[at+6] &= 0xfe
		out[at+6] |= (k >> 5) & 1
		out[at+7] &= 0x7f
		out[at+7] |= (k << 4) & 128
	}
	result := make([]byte, 0, len(out)+1)
	result = append(result, key[len(key)-1])
	return append(result, out...)
}
