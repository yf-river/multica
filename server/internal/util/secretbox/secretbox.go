// Package secretbox 提供用于"静态存储的机密"的带认证对称加密——目前主要是 Lark 的
// app_secret，以及未来任何不能以明文出现在数据库 dump 里的租户级机密列（MUL-2671 §4.4）。
//
// 构造：AES-256-GCM，密文前拼接每条消息随机生成的 12 字节 nonce。GCM 同时提供
// 机密性与完整性，因此被篡改的行会解密报错，而不是悄悄变成乱码明文。
//
// 密钥：32 字节。从环境变量以 base64 加载（LoadKey）。本轮迭代不支持轮换——等生产里
// 出现多把密钥时，再给密文加单字节前缀作为密钥 id；目前所有密文都由唯一一把当前
// 主密钥加解密。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

// KeySize 是主密钥的必需字节数（AES-256）。
const KeySize = 32

// ErrInvalidKey 是 New 在密钥长度不是 KeySize 时返回的错误。
var ErrInvalidKey = errors.New("secretbox: key must be 32 bytes")

// ErrCiphertextTooShort 在 Open 的输入小于 nonce + GCM tag 开销时返回。
var ErrCiphertextTooShort = errors.New("secretbox: ciphertext too short")

// Box 用一把固定的主密钥加密/解密字节切片。构造后 Box 可安全并发使用——
// cipher.AEAD 本身是 goroutine 安全的。
type Box struct {
	aead cipher.AEAD
}

// New 构造一个绑定给定 32 字节主密钥的 Box。调用方应在进程生命周期内持有返回的
// *Box；每次请求都重建会无谓地重复派生 AES 轮密钥。
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: cipher.NewGCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal 加密明文并返回 nonce || ciphertext || tag。nonce 每次调用随机生成；
// 调用方绝不能把输出当确定性结果缓存或复用（例如别拿密文当索引——同一明文的
// 两次 Seal 会产生不同字节）。
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: read nonce: %w", err)
	}
	// b.aead.Seal 会把密文+tag 追加到第一个参数后面；这里传 nonce，让调用方
	// 拿到的是按 nonce||ciphertext||tag 排布的单块连续切片，Open 再切开。
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open 逆转 Seal。输入畸形或被篡改时返回 ErrCiphertextTooShort 或（GCM 的）认证错误。
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns+b.aead.Overhead() {
		return nil, ErrCiphertextTooShort
	}
	nonce, ciphertext := sealed[:ns], sealed[ns:]
	return b.aead.Open(nil, nonce, ciphertext, nil)
}

// LoadKey 从指定环境变量读取 base64 编码的 32 字节密钥。解码后长度不是 KeySize
// 时返回 ErrInvalidKey。空的环境变量值视为"未配置"，以清晰错误浮出，而不是
// 悄悄用全零密钥。
func LoadKey(envVar string) ([]byte, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil, fmt.Errorf("secretbox: %s is not set", envVar)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %s is not valid base64: %w", envVar, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: %s decodes to %d bytes, expected %d", envVar, len(key), KeySize)
	}
	return key, nil
}
