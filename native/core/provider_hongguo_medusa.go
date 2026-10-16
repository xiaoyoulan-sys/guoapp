package core

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// 字节系签名全套自 hongguo.cjs 移植：x-gorgon 走变种 RC4，x-helios 走 34 轮表扩展，
// x-medusa 走自定义 AES 变体。随机数参与签名，服务端回验一致性。

func hongguoRandom32() uint32 {
	var buffer [4]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return uint32(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint32(buffer[:])
}

func hongguoXmxor(data, key []byte) []byte {
	encoded := make([]byte, len(data))
	for i := 0; i < len(data); i++ {
		at := (i * 4) & 28
		d0, d1 := key[at], key[at+1]
		d2 := (data[i]<<4 | data[i]>>4) + d0
		d2 = ^d2 ^ d1
		d2 = d2<<3 | d2>>5
		d2 = d2 + d1
		d2 = d2 ^ d0
		encoded[len(data)-i-1] = ^d2
	}
	last := encoded[len(encoded)-1] ^ encoded[len(encoded)-2]
	first := encoded[0]
	encoded[0] = ^last + first
	encoded[1] = (encoded[0] ^ encoded[len(encoded)-1] ^ 254) + encoded[1]
	encoded[2] = encoded[2] + ((last - first) ^ (encoded[1]<<3 | encoded[1]>>5) ^ 2)
	for i := 0; i < len(encoded)-4; i++ {
		temp := (encoded[i+2]<<3 | encoded[i+2]>>5) ^ encoded[i+1] ^ byte(i+3)
		encoded[i+3] = ^temp + encoded[i+3]
	}
	encoded[len(encoded)-1] ^= encoded[len(encoded)-2]
	sum := 0
	for i := 0; i < len(encoded)-1; i++ {
		sum += int(encoded[i+1])
	}
	encoded[0] = (encoded[0] ^ encoded[1]) + byte(sum)
	return encoded
}

func hongguoKeyHash(signKey []byte, random uint32) ([32]byte, []byte) {
	seed := make([]byte, 4)
	binary.LittleEndian.PutUint32(seed, random)
	input := make([]byte, 0, len(signKey)*2+4)
	input = append(input, signKey...)
	input = append(input, seed...)
	input = append(input, signKey...)
	hash := hongguoSM3(input)
	d1 := (random >> 16) & 255
	d2 := (d1<<11 | random>>24) ^ (d1 >> 5) ^ d1
	d2 = ^d2
	material := make([]byte, 4)
	binary.LittleEndian.PutUint32(material, d2)
	return hash, material
}

func hongguoBuildMedusa(rawURL string, body []byte, khronos uint32, deviceID, version string) string {
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
	queryBodyTS := hongguoHashF13(querySM3[:], bodyMD5, ts, khronos)

	var nested []byte
	nested = hongguoProtoAppendSint(nested, 1, 111)
	nested = hongguoProtoAppendSint(nested, 2, 10)
	nested = hongguoProtoAppendSint(nested, 3, 694367)
	nested = hongguoProtoAppendSint(nested, 5, 586952199)

	messageRand := hongguoRandom32()
	envLaunch := int64(hongguoRandom32()%21) + 100
	envPid := int64(hongguoRandom32()%2000) + 10001
	var timeEnv []byte
	timeEnv = hongguoProtoAppendSint(timeEnv, 1, time.Now().Unix())
	timeEnv = hongguoProtoAppendSint(timeEnv, 2, -2)
	timeEnv = hongguoProtoAppendSint(timeEnv, 4, 200)
	var env []byte
	env = hongguoProtoAppendSint(env, 1, envLaunch)
	env = hongguoProtoAppendSint(env, 2, 146331399)
	env = hongguoProtoAppendSint(env, 3, 146331396)
	env = hongguoProtoAppendSint(env, 5, 7)
	env = hongguoProtoAppendString(env, 6, "v04.06.04.03-bugfix")
	env = hongguoProtoAppendSint(env, 7, envPid)
	env = hongguoProtoAppendBytes(env, 12, hongguoDeviceProto(deviceID, version))
	env = hongguoProtoAppendBytes(env, 13, timeEnv)
	env = hongguoProtoAppendString(env, 14, version)

	queryHashInput := make([]byte, 0, len(query)+len(bodyMD5)+4)
	queryHashInput = append(queryHashInput, query...)
	queryHashInput = append(queryHashInput, bodyMD5...)
	queryHashInput = append(queryHashInput, "none"...)
	queryHash := hongguoSM3(queryHashInput)

	signKey, _ := hex.DecodeString("f7e85ffad7d7dc3bd62ac87057cf6118")
	var message []byte
	message = hongguoProtoAppendBytes(message, 1, signKey)
	message = hongguoProtoAppendSint(message, 2, 3)
	message = hongguoProtoAppendSint(message, 3, int64(messageRand))
	message = hongguoProtoAppendString(message, 4, "8662")
	message = hongguoProtoAppendString(message, 5, deviceID)
	message = hongguoProtoAppendString(message, 6, "1588093228")
	message = hongguoProtoAppendString(message, 7, version)
	message = hongguoProtoAppendString(message, 8, "v04.06.04-ml-android")
	message = hongguoProtoAppendSint(message, 9, 67503104)
	message = hongguoProtoAppendBytes(message, 10, []byte{0x40, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	message = hongguoProtoAppendSint(message, 12, int64(khronos))
	message = hongguoProtoAppendBytes(message, 13, queryBodyTS)
	message = hongguoProtoAppendBytes(message, 14, querySM3[:6])
	message = hongguoProtoAppendBytes(message, 15, nested)
	message = hongguoProtoAppendString(message, 16, "AXYQOS6n2m60x1fVZHIrH3iol")
	message = hongguoProtoAppendSint(message, 17, int64(khronos))
	message = hongguoProtoAppendBytes(message, 19, queryHash[:])
	message = hongguoProtoAppendString(message, 20, "none")
	message = hongguoProtoAppendSint(message, 21, 312)
	message = hongguoProtoAppendBytes(message, 23, env)
	message = hongguoProtoAppendString(message, 24, `{"cmr":16777216,"cmr2":16777216,"un_h":1879194040,"vpn":0,"kd":0,"fkd":3672518972,"pd":-1872573247,"dyn":"","do":0,"tk":true}`)

	random := hongguoRandom32()
	keyMaterial, _ := hex.DecodeString("8ebdfa3806ecc5cee79423e6029ed82540bc2218bb7eaef71cb691f7aa8aa2f5")
	hash, seed := hongguoKeyHash(keyMaterial, random)
	transformed := hongguoXmxor(message, hash[:])
	combined := make([]byte, 0, len(transformed)+8)
	combined = append(combined, 0x40, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	combined = append(combined, transformed...)
	for left, right := 0, len(combined)-1; left < right; left, right = left+1, right-1 {
		combined[left], combined[right] = combined[right], combined[left]
	}
	for i := range combined {
		combined[i] ^= seed[3-i%4]
	}

	check := uint32(querySM3[0]&63)<<14 | 0x18000001 | uint32(queryBodyTS[0]&63)<<8
	xmRand := hongguoRandom32()
	packed := make([]byte, 0, len(combined)+11)
	packed = append(packed, 0x35)
	buffer := make([]byte, 4)
	binary.LittleEndian.PutUint32(buffer, xmRand)
	packed = append(packed, buffer...)
	binary.LittleEndian.PutUint32(buffer, check)
	packed = append(packed, buffer...)
	packed = append(packed, combined...)
	packed = append(packed, byte(random>>16), byte(random>>24))

	key, _ := hex.DecodeString("f1593376766ea98d34f31b057a9d5be4")
	iv, _ := hex.DecodeString("1fe109a4125283f418de9e051a969e12")
	encrypted := newHongguoAesV3(key, khronos).encrypt(packed, iv)

	versionBytes, _ := hex.DecodeString("03000000f7e85ffad7d7dc3bd62ac87057cf6118")
	head := make([]byte, 20)
	for i := 0; i < 20; i += 4 {
		binary.LittleEndian.PutUint32(head[i:], binary.LittleEndian.Uint32(versionBytes[i:])^khronos)
	}
	out := make([]byte, 0, len(head)+len(encrypted)+4)
	out = append(out, head...)
	out = append(out, byte(random&255), byte(random>>8&255), 0, 1)
	out = append(out, encrypted...)
	return base64.StdEncoding.EncodeToString(out)
}

func hongguoHelios(khronos uint32) string {
	random := hongguoRandom32()
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:], random)
	copy(data[4:], "8662")
	digest := md5.Sum(data)
	ascii := []byte(hex.EncodeToString(digest[:]))
	var words [4]uint64
	for i := 0; i < 4; i++ {
		words[i] = binary.LittleEndian.Uint64(ascii[i*8:])
	}
	table := make([]uint64, 0, 35)
	table = append(table, words[0])
	b0, b8 := words[0], words[1]
	queue := []uint64{words[2], words[3]}
	for i := 0; i < 34; i++ {
		x8 := bits.RotateLeft64(b8, -8) + b0
		x8 ^= uint64(i)
		queue = append(queue, x8)
		x8 ^= bits.RotateLeft64(b0, 61)
		table = append(table, x8)
		b0 = x8
		b8 = queue[0]
		queue = queue[1:]
	}
	text := []byte(strconv.FormatUint(uint64(khronos), 10) + "-1588093228-8662")
	size := (len(text) + 1 + 15) / 16 * 16
	pad := make([]byte, size)
	fill := byte(16 - len(text)%16)
	for i := range pad {
		pad[i] = fill
	}
	copy(pad, text)
	output := make([]byte, 0, size)
	var block [16]byte
	for at := 0; at < len(pad); at += 16 {
		a := binary.LittleEndian.Uint64(pad[at:])
		b := binary.LittleEndian.Uint64(pad[at+8:])
		for i := 0; i < 34; i++ {
			b = table[i] ^ (a + bits.RotateLeft64(b, -8))
			a = b ^ bits.RotateLeft64(a, 61)
		}
		binary.LittleEndian.PutUint64(block[0:], a)
		binary.LittleEndian.PutUint64(block[8:], b)
		output = append(output, block[:]...)
	}
	head := make([]byte, 4)
	binary.LittleEndian.PutUint32(head, random)
	return base64.StdEncoding.EncodeToString(append(head, output...))
}

func hongguoReverseBits(value byte) byte {
	var result byte
	for i := 0; i < 8; i++ {
		result = result<<1 | (value>>uint(i))&1
	}
	return result
}

func hongguoRC4Gorgon(data, key []byte) []byte {
	var box [256]byte
	for i := range box {
		box[i] = byte(i)
	}
	j := byte(0)
	for i := 0; i < 256; i++ {
		j = j + box[i] + key[i%len(key)]
		box[i] = box[j]
	}
	out := make([]byte, len(data))
	i := 0
	j = 0
	for index, value := range data {
		i++
		x := box[i]
		j += x
		y := box[j&255]
		box[i] = y
		out[index] = value ^ box[(y+y)&255]
	}
	return out
}

func hongguoXGorgon(query string, body []byte, timestamp, random uint32) string {
	input := make([]byte, 0, 20)
	queryHash := md5.Sum([]byte(query))
	input = append(input, queryHash[:4]...)
	if body != nil {
		bodyHash := md5.Sum(body)
		input = append(input, bodyHash[:4]...)
	} else {
		input = append(input, make([]byte, 4)...)
	}
	input = append(input, make([]byte, 4)...)
	sdk := make([]byte, 4)
	binary.LittleEndian.PutUint32(sdk, 67503104)
	input = append(input, sdk...)
	stamp := make([]byte, 4)
	binary.BigEndian.PutUint32(stamp, timestamp)
	input = append(input, stamp...)
	key := []byte{0x4a, 0x40, 0x16, byte(random >> 8 & 255), 0x47, 0x6c, 0x01, byte(random & 255)}
	out := hongguoRC4Gorgon(input, key)
	for i := 0; i < len(out); i++ {
		value := out[i]>>4 | out[i]<<4
		next := out[0]
		if i+1 < len(out) {
			next = out[i+1]
		}
		out[i] = ^(hongguoReverseBits((next^value)&255) ^ 20)
	}
	prefix := []byte{0x84, 0x04, byte(random & 255), byte(random >> 8 & 255), 0x40, 0x01}
	return hex.EncodeToString(append(prefix, out...))
}
