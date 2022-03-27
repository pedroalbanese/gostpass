// Copyright 2016 The Sandpass Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package kdbcrypt encrypts and decrypts data using the KeePass1 encryption scheme.
package kdbcrypt // import "github.com/pedroalbanese/gostpass/pkg/kdbcrypt"

import (
	"github.com/pedroalbanese/gogost/gost3412128"
	"github.com/pedroalbanese/gogost/gost341264"
	_ "github.com/pedroalbanese/gogost/gost28147"
	"github.com/pedroalbanese/gogost/gost34112012256"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/ioutil"
	"sync"

	"github.com/pedroalbanese/gostpass/pkg/cipherio"
	"github.com/pedroalbanese/gostpass/pkg/padding"
)

// Errors
var (
	ErrUnknownCipher = errors.New("keepass: unknown cipher")
	ErrSize          = errors.New("keepass: data size not a multiple of 16")
)

// Block size in bytes.
const BlockSize = 16

// Params specifies the encryption/decryption values.
type Params struct {
	Key         Key
	ComputedKey ComputedKey // if non-nil, this will be used instead of Key.
	Cipher      Cipher
	IV          [16]byte
}

// A Key is the set of parameters used to build the cipher key.
type Key struct {
	Password        []byte // optional
	KeyFileHash     []byte // must be nil or length 16
	MasterSeed      [16]byte
	TransformSeed   [32]byte
	TransformRounds uint32
}

// Compute derives the actual cipher key from the user-specifiable parameters.
func (k *Key) Compute() ComputedKey {
	sum := gost34112012256.New()

	sum.Write(k.MasterSeed[:])

	base := k.baseHash()
	var wg sync.WaitGroup
	wg.Add(2)
	var tk [sha256.Size]byte
	go transformKeyBlock(&wg, tk[:gost3412128.BlockSize], base[:gost3412128.BlockSize], k.TransformSeed[:], k.TransformRounds)
	go transformKeyBlock(&wg, tk[gost3412128.BlockSize:], base[gost3412128.BlockSize:], k.TransformSeed[:], k.TransformRounds)
	wg.Wait()
	tk = sha256.Sum256(tk[:])
	sum.Write(tk[:])

	return sum.Sum(nil)
}

// baseHash returns the key's hash prior to encryption rounds.
func (k *Key) baseHash() [sha256.Size]byte {
	if len(k.KeyFileHash) == 0 {
		return sha256.Sum256(k.Password)
	}
	if len(k.Password) == 0 {
		var a [sha256.Size]byte
		copy(a[:], k.KeyFileHash)
		return a
	}
	h := gost34112012256.New()
	p := sha256.Sum256(k.Password)
	h.Write(p[:])
	h.Write(k.KeyFileHash)
	var a [sha256.Size]byte
	h.Sum(a[:0])
	return a
}

// transformKeyBlock applies rounds of Magma encryption using key seed to src and stores the result in dst.
func transformKeyBlock(wg *sync.WaitGroup, dst, src, seed []byte, rounds uint32) {
	dst = dst[:gost3412128.BlockSize]
	copy(dst, src)
	c := gost341264.NewCipher(seed)

	for i := uint32(0); i < rounds; i++ {
		c.Encrypt(dst, dst)
	}
	wg.Done()
}

// A ComputedKey is the encryption key that is directly passed to the
// cipher, derived from a Key.  Since computing a key is slow by design,
// if you intend to decrypt and encrypt a database multiple times in
// quick succession, this will be much faster.
type ComputedKey []byte

// Cipher is a cipher algorithm.
type Cipher int

// Available ciphers
const (
	RijndaelCipher Cipher = iota
	TwofishCipher
)

func (c Cipher) cipher(key ComputedKey) (*gost3412128.Cipher) {
	return gost3412128.NewCipher([]byte(key))
}

// NewEncrypter creates a new writer that encrypts to w.  Closing the
// new writer writes the final, padded block but does not close w.
func NewEncrypter(w io.Writer, params *Params) (io.WriteCloser, error) {
	ck := params.ComputedKey
	if ck == nil {
		ck = params.Key.Compute()
	}
	ciph := params.Cipher.cipher(ck)

	e := cipher.NewCBCEncrypter(ciph, params.IV[:])
	return cipherio.NewWriter(w, e, padding.PKCS7), nil
}

// NewDecrypter creates a new reader that decrypts and strips padding from r.
func NewDecrypter(r io.Reader, params *Params) (io.Reader, error) {
	ck := params.ComputedKey
	if ck == nil {
		ck = params.Key.Compute()
	}
	ciph := params.Cipher.cipher(ck)

	d := cipher.NewCBCDecrypter(ciph, params.IV[:])
	return cipherio.NewReader(r, d, padding.PKCS7), nil
}

// ReadKeyFile reads a key file and returns its hash for use in a Key.
func ReadKeyFile(r io.Reader) ([]byte, error) {
	const maxSize = 64
	data, err := ioutil.ReadAll(&io.LimitedReader{R: r, N: maxSize + 1})
	if err != nil {
		return data, err
	}
	switch len(data) {
	case 32:
		return data, nil
	case 64:
		h := make([]byte, hex.DecodedLen(len(data)))
		if _, err := hex.Decode(h, data); err == nil {
			return h, nil
		}
	}
	s := gost34112012256.New()
	s.Write(data[:])
	if _, err := io.Copy(s, r); err != nil {
		return nil, err
	}
	return s.Sum(nil), nil
}