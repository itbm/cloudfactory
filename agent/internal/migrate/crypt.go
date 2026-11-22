package migrate

import (
	"crypto/rand"
	"crypto/sha512"
	"strings"
)

// cryptAlphabet is the alphabet of the crypt hashes.
const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// sha512Order is the order of the bytes of the digest in a SHA-512-crypt hash: three bytes
// give four characters.
var sha512Order = [21][3]int{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
	{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
	{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
}

// hashPassword gives a SHA-512-crypt hash of a password with a new salt, for a backup that has
// the password as text. The mail server checks this hash (the rule "mailPasswordHash"), and the
// text of the password does not leave the agent.
func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	for i, b := range salt {
		salt[i] = cryptAlphabet[int(b)%len(cryptAlphabet)]
	}
	return sha512Crypt(password, string(salt))
}

// sha512Crypt is the SHA-512-crypt function of glibc ("$6$"), with 5000 rounds and a salt of
// up to 16 characters.
func sha512Crypt(password, salt string) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	p, s := []byte(password), []byte(salt)

	alt := sha512.New()
	alt.Write(p)
	alt.Write(s)
	alt.Write(p)
	b := alt.Sum(nil)

	a := sha512.New()
	a.Write(p)
	a.Write(s)
	n := len(p)
	for ; n > 64; n -= 64 {
		a.Write(b)
	}
	a.Write(b[:n])
	for n := len(p); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(b)
		} else {
			a.Write(p)
		}
	}
	digest := a.Sum(nil)

	dp := sha512.New()
	for range len(p) {
		dp.Write(p)
	}
	pSeq := repeatTo(dp.Sum(nil), len(p))

	ds := sha512.New()
	for range 16 + int(digest[0]) {
		ds.Write(s)
	}
	sSeq := repeatTo(ds.Sum(nil), len(s))

	for i := range 5000 {
		c := sha512.New()
		if i&1 != 0 {
			c.Write(pSeq)
		} else {
			c.Write(digest)
		}
		if i%3 != 0 {
			c.Write(sSeq)
		}
		if i%7 != 0 {
			c.Write(pSeq)
		}
		if i&1 != 0 {
			c.Write(digest)
		} else {
			c.Write(pSeq)
		}
		digest = c.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$" + salt + "$")
	encode := func(b2, b1, b0 byte, chars int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for range chars {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	for _, o := range sha512Order {
		encode(digest[o[0]], digest[o[1]], digest[o[2]], 4)
	}
	encode(0, 0, digest[63], 2)
	return out.String()
}

// repeatTo gives b repeated to n bytes.
func repeatTo(b []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, b[:min(len(b), n-len(out))]...)
	}
	return out
}
