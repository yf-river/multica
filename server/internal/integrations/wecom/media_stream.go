// media_stream.go —— 一个「不必整体塞进内存」的附件处理路径。
//
// media_download.go 里的缓冲式路径会把每个附件占两份内存：下载下来的整份密文，
// 再算上解密后的整份明文，上传期间两者同时活着。引擎把并发媒体解析上限设为
// 8、资源上限 100 MiB，所以一个再普通不过的工作区能触到的最坏情况——四个人
// 每人发两个大文件——就是 8 × 200 MiB 的活跃堆内存。在自托管机器上这是直接
// OOM，而被杀掉的进程还同时服务着飞书、Slack 和钉钉。那条路径如今只作为「没
// 有 UploadStream 的存储后端」的兜底保留；两个随附的后端都有 UploadStream，所
// 以真实部署跑的都是本文件。
//
// 换成只落一个临时文件：密文根本不上磁盘——它从 socket 流进来、逐块解密写进
// 文件，上传再从文件读回去。每个附件的高峰堆占用缩成一个缓冲区，本会被相乘的
// 那个数字改由磁盘来兜底。
//
// 临时文件也是「流式上传」能成立的前提。S3Storage.UploadStream 要求精确的
// ContentLength，而明文长度等于密文长度减去一个「读完最后一块才知道」的填充
// ——在上传必须开始的这一刻根本无法预知。磁盘上的文件早就回答了这个问题：
// 它的大小就是长度。
//
// 临时文件按 0600 创建、所有路径都会删除它。它们装的是解密后的附件内容，所以
// 权限设置属于功能的一部分，不是清洁卫生问题。
package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"os"
)

// mediaStreamChunk 是每次解密多少密文的粒度。大到让系统调用开销对 100 MiB 的
// 文件可以忽略不计，小到让这些缓冲区在进程已持有的其他内存旁边显得无足轻重。
const mediaStreamChunk = 256 << 10

// decryptToFile 从 src 读密文、解密进一个新的临时文件，返回定位在文件开头
// 的句柄以及明文长度。关闭与删除由调用方负责。
//
// CBC 必须先见尾才能信头——填充在最后一块上——所以最后一块要被扣住，等源被
// 读尽后再去填充。它之前的每一块都边读边写。
func decryptToFile(aesKey string, src io.Reader, dir string) (f *os.File, size int64, err error) {
	key, err := decodeMediaAESKey(aesKey)
	if err != nil {
		return nil, 0, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, fmt.Errorf("wecom: media cipher: %w", err)
	}
	// IV 取 key 的前 16 字节，和企业微信自家库的做法一致。
	mode := cipher.NewCBCDecrypter(block, key[:aes.BlockSize])

	out, err := os.CreateTemp(dir, "wecom-media-*.bin")
	if err != nil {
		return nil, 0, fmt.Errorf("wecom: media temp file: %w", err)
	}
	// 现在就 unlink：文件仍能通过句柄读，而进程一放手（包括崩溃）它立刻消失。
	// 绝不给任何人留下装了解密附件内容的文件可捡。
	if err := os.Remove(out.Name()); err != nil {
		out.Close()
		return nil, 0, fmt.Errorf("wecom: media temp file: %w", err)
	}
	if err := out.Chmod(0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		// 尽力而为：文件已经 unlink 了，所以这里只是双保险。
		_ = err
	}
	defer func() {
		if err != nil {
			out.Close()
		}
	}()

	buf := make([]byte, mediaStreamChunk)
	// tail 负责扣住明文末尾最多 mediaPadBlock 字节：PKCS#7 填充最长 32 字节、
	// 会横跨「两个」AES 块——填充块和密码块在这里不是一个尺寸，这正是
	// media_crypt.go 记录过的那个陷阱。只扣一个 AES 块的话，只有 ≤16 的填充
	// 才能正确去填充，也就是大约只有一半文件能对上。
	tail := make([]byte, 0, mediaPadBlock+aes.BlockSize)
	var carry []byte // 尚未凑到块边界上的密文字节
	var written int64

	emit := func(plain []byte) error {
		tail = append(tail, plain...)
		if len(tail) <= mediaPadBlock {
			return nil
		}
		cut := len(tail) - mediaPadBlock
		if _, werr := out.Write(tail[:cut]); werr != nil {
			return werr
		}
		written += int64(cut)
		tail = append(tail[:0], tail[cut:]...)
		return nil
	}

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			usable := len(carry) - len(carry)%aes.BlockSize
			if usable > 0 {
				chunk := carry[:usable]
				mode.CryptBlocks(chunk, chunk)
				if err = emit(chunk); err != nil {
					return nil, 0, fmt.Errorf("wecom: media decrypt: write: %w", err)
				}
				carry = append(carry[:0], carry[usable:]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("wecom: media decrypt: read: %w", readErr)
		}
	}

	if len(carry) != 0 {
		return nil, 0, fmt.Errorf("wecom: media ciphertext is not a multiple of the block size (%d trailing bytes)", len(carry))
	}
	if written == 0 && len(tail) == 0 {
		return nil, 0, errors.New("wecom: media ciphertext is empty")
	}

	// 填充就在扣下的那段里，unpadMedia 会逐字节校验——尾巴对不上说明密钥错或
	// 密文被截断，它前面的文件内容同样不可信。
	unpadded, err := unpadMedia(tail)
	if err != nil {
		return nil, 0, err
	}
	if len(unpadded) > 0 {
		if _, err = out.Write(unpadded); err != nil {
			return nil, 0, fmt.Errorf("wecom: media decrypt: write: %w", err)
		}
		written += int64(len(unpadded))
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("wecom: media decrypt: rewind: %w", err)
	}
	return out, written, nil
}

// peekFile 从 f 头部读最多 n 字节然后倒回，让需要嗅探内容类型的调用方不必
// 把整个文件都读进内存。
func peekFile(f *os.File, n int) ([]byte, error) {
	head := make([]byte, n)
	read, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return head[:read], nil
}
