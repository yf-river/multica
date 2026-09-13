// media_crypt.go —— 回调里 aeskey 解开的是什么。
//
// 一条带图片、文件或视频的智能机器人消息会给机器人两样东西：一个五分钟有效
// 的腾讯 COS URL，以及 URL 背后字节流所用的加密密钥。长连接模式给每个 URL
// 现造一把新 key，这正是它与回调模式那套「全部署共用一把 EncodingAESKey」
// 的区别——没有任何东西要配置、要轮换，但也意味着帧上的 key 一旦不可用，
// 就没有任何兜底可退。
//
// 算法是 AES-256-CBC，IV 取自 key 的前段，明文按 32 字节整倍数做 PKCS#7
// 填充（developer.work.weixin.qq.com/document/path/101463）。
package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// mediaAESKeyBytes 是解码后密钥的长度，正好满足 AES-256 的要求。
const mediaAESKeyBytes = 32

// mediaPadBlock 是明文要填充到的块大小。它不是 AES 的块大小，而这个落差正
// 是陷阱所在：AES 按 16 字节一块工作，所以照 cipher 写的 PKCS#7 去填充器会
// 拒绝任何超过 16 的 pad——而文件长度恰好是 32 的倍数时，会填充一个完整的
// 32 字节块。这类负载出现得足够频繁，让一个这样的去填充器在冒烟测试里看着
// 正常、一到生产就翻车。
const mediaPadBlock = 32

// decryptMedia 把一段下载回来的密文还原成用户发来的那个文件。所有失败都
// 当作错误返回而不是猜一个结果：这里答错了，附件打开就是乱码，比附件老实
// 地缺失更糟。
func decryptMedia(aesKey string, ciphertext []byte) ([]byte, error) {
	key, err := decodeMediaAESKey(aesKey)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("wecom: media ciphertext is empty")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("wecom: media ciphertext is %d bytes, not a multiple of the %d-byte AES block", len(ciphertext), aes.BlockSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wecom: media cipher: %w", err)
	}
	plain := make([]byte, len(ciphertext))
	// IV 直接取 key 的前段。拿密钥材料当 IV 是企业微信自己的选择，不是我们的，
	// 我们只需要跟着它做、对齐结果即可。
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plain, ciphertext)
	return unpadMedia(plain)
}

// decodeMediaAESKey 解码帧上携带的 base64。企业微信自身的各种界面上，既见过
// 带填充的 44 字符形式，也见过不带填充的 43 字符形式，所以两种都收；凡解不出
// 恰好 32 字节的一律拒绝，绝不拉伸或截断凑数。
func decodeMediaAESKey(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("wecom: media aeskey is empty")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == mediaAESKeyBytes {
			return key, nil
		}
	}
	return nil, fmt.Errorf("wecom: media aeskey does not decode to %d bytes", mediaAESKeyBytes)
}

// unpadMedia 剥掉 PKCS#7 尾巴，并逐字节校验它。尾巴对不上说明 key 是错的
// 或密文被截断——无论哪种，它前面的字节都不能被当作可信的文件内容。
func unpadMedia(plain []byte) ([]byte, error) {
	n := len(plain)
	if n == 0 {
		return nil, errors.New("wecom: media plaintext is empty")
	}
	pad := int(plain[n-1])
	if pad < 1 || pad > mediaPadBlock || pad > n {
		return nil, fmt.Errorf("wecom: media padding length %d is out of range (1..%d)", pad, mediaPadBlock)
	}
	for _, b := range plain[n-pad:] {
		if int(b) != pad {
			return nil, errors.New("wecom: media padding bytes disagree; wrong key or truncated body")
		}
	}
	return plain[:n-pad], nil
}
