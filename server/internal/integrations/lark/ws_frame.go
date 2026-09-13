// ws_frame.go：飞书/钉钉长连接二进制 Frame 信封的重新实现，与官方 SDK 线上
// 字节完全兼容。
//
// 编码出的字节与官方 SDK（github.com/larksuite/oapi-sdk-go/v3/ws）的 Frame
// 逐字节一致——这一点很关键，因为飞书服务器会拒收缺少必填字段的帧。SDK 由
// pbbp2.proto 用 proto2 + gogo 的 `req` 语义生成，所以 SeqID / LogID /
// Service / Method 无条件发出（即使为 0）；opt 字符串字段（PayloadEncoding /
// PayloadType / LogIDNew）也因 gogo 生成代码跳过 opt 字符串零值守卫而无条件
// 发出。只有 Payload 走 `nil` 守卫。参考 SDK 的 pbbp2.pb.go
// MarshalToSizedBuffer；与它对不上会悄悄破坏数据流——因为用我们自己的
// UnmarshalFrame 做往返会掩盖这个 bug。
//
// 我们用手写 google.golang.org/protobuf/encoding/protowire 来重编码，而不是
// import 官方 SDK 的 pbbp2 包：SDK 的传递依赖树很大（完整开放平台 client、
// 每个 API 面的生成代码），而我们只需要这一个消息类型。为一个 9 字段的消息
// 手写编解码，规模可控，依赖面却小很多。ws_frame_test.go 里的 golden 测试
// 锁定了 SDK 对若干典型帧会产出的确切字节序列——那才是承重的兼容性校验。
package lark

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// FrameMethodControl 标识 Method=Control(0) 的帧。控制帧承载 ping/pong
	// 和服务器下推的 ClientConfig 更新；从不携带入站事件负载。
	FrameMethodControl int32 = 0

	// FrameMethodData 标识 Method=Data(1) 的帧。数据帧携带真正的事件负载
	// （im.message.receive_v1、卡片交互等），并要求回 ACK。
	FrameMethodData int32 = 1
)

// FrameHeaderType 相关的枚举值：飞书放在 Frame header 中 `type` 键下的取值，
// 驱动每帧的路由逻辑。
const (
	FrameHeaderTypeKey   = "type"
	FrameHeaderTypeEvent = "event"
	FrameHeaderTypeCard  = "card"
	FrameHeaderTypePing  = "ping"
	FrameHeaderTypePong  = "pong"

	// FrameHeaderMessageIDKey 是飞书给每个数据帧打的去重/分块键；ACK 时原样
	// 带回，方便服务器关联。
	FrameHeaderMessageIDKey = "message_id"

	// FrameHeaderSumKey / FrameHeaderSeqKey 携带多帧负载的分块元信息（sum>1
	// 表示总共 N 块，seq 表示第几块）。ws_chunk_assembler.go 里的分块组装器
	// 用它们把多个 Frame 重组成单个 JSON 负载后再交给 decoder。
	FrameHeaderSumKey = "sum"
	FrameHeaderSeqKey = "seq"
)

// FrameHeader 是 Frame.Headers 里的一个（key, value）对，等价于 SDK 的
// pbbp2.Header。
type FrameHeader struct {
	Key   string
	Value string
}

// Frame 镜像 pbbp2.Frame，字段编号与 SDK proto 一致，保证线上字节与
// oapi-sdk-go 产出逐字节相同。
//
// Marshal 实现与 SDK 的 gogo 生成代码一致：SeqID、LogID、Service、Method 作为
// proto2 required 字段无条件发出；PayloadEncoding、PayloadType、LogIDNew 为空
// 时也以零长度值无条件发出（匹配生成代码的无条件 copy + length-encoding）；
// Payload 仅在非 nil 时发出。golden 测试锁定精确字节序列。
type Frame struct {
	SeqID           uint64        // proto field 1 (req)
	LogID           uint64        // proto field 2 (req)
	Service         int32         // proto field 3 (req)
	Method          int32         // proto field 4 (req)
	Headers         []FrameHeader // proto field 5 (rep)
	PayloadEncoding string        // proto field 6 (opt)
	PayloadType     string        // proto field 7 (opt)
	Payload         []byte        // proto field 8 (opt)
	LogIDNew        string        // proto field 9 (opt)
}

// HeaderValue 返回首个与给定 key 匹配的 header 值，缺则返回 ""。飞书把
// headers 当扁平 map 用，但 SDK 的 proto schema 是 repeated 字段——重复时
// 按“首个优先”处理，因为 SDK 实际也是这么做的。
func (f *Frame) HeaderValue(key string) string {
	for _, h := range f.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

// Marshal 把帧编码成飞书期望的线上格式。返回的字节将作为 WebSocket 二进制
// 负载原样发送。
//
// 字段发射顺序与 SDK 的 MarshalToSizedBuffer 反向构建一致（最终字节流里为
// 字段 1→9）。不要改动“哪些字段无条件发射”的逻辑，除非重新对照 SDK 生成的
// MarshalToSizedBuffer——它才是线上形态的权威，一旦背离，只有到飞书服务器在
// 生产环境开始丢帧时才会暴露。
func (f *Frame) Marshal() []byte {
	buf := make([]byte, 0, 64+len(f.Payload))

	// 必填字段（proto2 req）：即便值是 0 也要发射 tag + varint。SDK 生成代码
	// 无条件写它们；飞书服务器读到缺任一必填字段的 Frame 会回
	// RequiredNotSetError 并丢弃该帧。
	buf = protowire.AppendTag(buf, 1, protowire.VarintType)
	buf = protowire.AppendVarint(buf, f.SeqID)
	buf = protowire.AppendTag(buf, 2, protowire.VarintType)
	buf = protowire.AppendVarint(buf, f.LogID)
	buf = protowire.AppendTag(buf, 3, protowire.VarintType)
	buf = protowire.AppendVarint(buf, uint64(uint32(f.Service)))
	buf = protowire.AppendTag(buf, 4, protowire.VarintType)
	buf = protowire.AppendVarint(buf, uint64(uint32(f.Method)))

	// repeated headers——每个 FrameHeader 发一条带长度前缀的条目。Headers
	// 为空时不发任何东西，与 SDK 守卫一致。
	for _, h := range f.Headers {
		buf = protowire.AppendTag(buf, 5, protowire.BytesType)
		buf = protowire.AppendVarint(buf, uint64(headerSize(h)))
		// Header.Key (field 1) 与 Header.Value (field 2) 都是 proto2 req——
		// 无条件发射。
		buf = protowire.AppendTag(buf, 1, protowire.BytesType)
		buf = protowire.AppendString(buf, h.Key)
		buf = protowire.AppendTag(buf, 2, protowire.BytesType)
		buf = protowire.AppendString(buf, h.Value)
	}

	// PayloadEncoding (field 6) —— gogo 生成代码无条件 copy 字符串，即使
	// len==0 也发 tag + 长度前缀。空串照样产出 tag + 零长度。
	buf = protowire.AppendTag(buf, 6, protowire.BytesType)
	buf = protowire.AppendString(buf, f.PayloadEncoding)

	// PayloadType (field 7) —— 与 PayloadEncoding 相同的无条件发射。
	buf = protowire.AppendTag(buf, 7, protowire.BytesType)
	buf = protowire.AppendString(buf, f.PayloadType)

	// Payload (field 8) —— SDK 用 `if m.Payload != nil`。我们镜像它：nil 时
	// 完全省略 tag；非 nil 时即使 len==0 也发射。
	if f.Payload != nil {
		buf = protowire.AppendTag(buf, 8, protowire.BytesType)
		buf = protowire.AppendBytes(buf, f.Payload)
	}

	// LogIDNew (field 9) —— 与 PayloadEncoding/Type 一样无条件发射。
	buf = protowire.AppendTag(buf, 9, protowire.BytesType)
	buf = protowire.AppendString(buf, f.LogIDNew)

	return buf
}

// headerSize 计算单个 FrameHeader 编码后的字节数。
func headerSize(h FrameHeader) int {
	// 两个字段都是 req 且总是发射；字节代价 = tag + length-prefix(len) + len。
	return protowire.SizeTag(1) + protowire.SizeBytes(len(h.Key)) +
		protowire.SizeTag(2) + protowire.SizeBytes(len(h.Value))
}

// UnmarshalFrame 把一条二进制 protobuf 消息解析成 Frame。未知字段被跳过
// （proto3 行为），这样服务器端新增 schema 不会破坏我们。截断/损坏的字节返回
// error，调用方（WS connector）会把该帧当作坏帧丢弃而不拆除连接。
func UnmarshalFrame(b []byte) (*Frame, error) {
	if len(b) == 0 {
		return nil, errors.New("ws frame: empty buffer")
	}
	f := &Frame{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return nil, fmt.Errorf("ws frame: consume tag: %w", err)
		}
		b = b[n:]
		switch num {
		case 1: // SeqID uint64
			if typ != protowire.VarintType {
				return nil, fmt.Errorf("ws frame: field 1 expects varint, got %v", typ)
			}
			v, m := protowire.ConsumeVarint(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume seq_id: %w", err)
			}
			f.SeqID = v
			b = b[m:]
		case 2: // LogID uint64
			if typ != protowire.VarintType {
				return nil, fmt.Errorf("ws frame: field 2 expects varint, got %v", typ)
			}
			v, m := protowire.ConsumeVarint(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume log_id: %w", err)
			}
			f.LogID = v
			b = b[m:]
		case 3: // Service int32
			if typ != protowire.VarintType {
				return nil, fmt.Errorf("ws frame: field 3 expects varint, got %v", typ)
			}
			v, m := protowire.ConsumeVarint(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume service: %w", err)
			}
			f.Service = int32(v)
			b = b[m:]
		case 4: // Method int32
			if typ != protowire.VarintType {
				return nil, fmt.Errorf("ws frame: field 4 expects varint, got %v", typ)
			}
			v, m := protowire.ConsumeVarint(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume method: %w", err)
			}
			f.Method = int32(v)
			b = b[m:]
		case 5: // Headers (repeated)
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("ws frame: field 5 expects bytes, got %v", typ)
			}
			hb, m := protowire.ConsumeBytes(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume header: %w", err)
			}
			h, herr := unmarshalHeader(hb)
			if herr != nil {
				return nil, herr
			}
			f.Headers = append(f.Headers, h)
			b = b[m:]
		case 6: // PayloadEncoding string
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("ws frame: field 6 expects bytes, got %v", typ)
			}
			s, m := protowire.ConsumeString(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume payload_encoding: %w", err)
			}
			f.PayloadEncoding = s
			b = b[m:]
		case 7: // PayloadType string
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("ws frame: field 7 expects bytes, got %v", typ)
			}
			s, m := protowire.ConsumeString(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume payload_type: %w", err)
			}
			f.PayloadType = s
			b = b[m:]
		case 8: // Payload bytes
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("ws frame: field 8 expects bytes, got %v", typ)
			}
			raw, m := protowire.ConsumeBytes(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume payload: %w", err)
			}
			// 拷贝一份，让 Frame 里的 Payload 能活得比输入缓冲更久
			// （ConsumeBytes 返回的是原缓冲的子切片）。
			f.Payload = append([]byte(nil), raw...)
			b = b[m:]
		case 9: // LogIDNew string
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("ws frame: field 9 expects bytes, got %v", typ)
			}
			s, m := protowire.ConsumeString(b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: consume log_id_new: %w", err)
			}
			f.LogIDNew = s
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("ws frame: skip unknown field %d: %w", num, err)
			}
			b = b[m:]
		}
	}
	return f, nil
}

// unmarshalHeader 解析 Header 子消息（字段 5 内嵌的内容）为一个 FrameHeader。
func unmarshalHeader(b []byte) (FrameHeader, error) {
	var h FrameHeader
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return FrameHeader{}, fmt.Errorf("ws frame: header tag: %w", err)
		}
		b = b[n:]
		switch num {
		case 1:
			if typ != protowire.BytesType {
				return FrameHeader{}, fmt.Errorf("ws frame: header.key expects bytes, got %v", typ)
			}
			s, m := protowire.ConsumeString(b)
			if err := protowire.ParseError(m); err != nil {
				return FrameHeader{}, fmt.Errorf("ws frame: header.key: %w", err)
			}
			h.Key = s
			b = b[m:]
		case 2:
			if typ != protowire.BytesType {
				return FrameHeader{}, fmt.Errorf("ws frame: header.value expects bytes, got %v", typ)
			}
			s, m := protowire.ConsumeString(b)
			if err := protowire.ParseError(m); err != nil {
				return FrameHeader{}, fmt.Errorf("ws frame: header.value: %w", err)
			}
			h.Value = s
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if err := protowire.ParseError(m); err != nil {
				return FrameHeader{}, fmt.Errorf("ws frame: skip header field %d: %w", num, err)
			}
			b = b[m:]
		}
	}
	return h, nil
}

// NewPingFrame 构建客户端保活帧。飞书长连接用的是应用层 ping（type=ping 的
// 二进制 Frame），而不是 WebSocket 协议层 PING——gorilla 的 WriteControl ping
// 会被无视。
func NewPingFrame(serviceID int32) *Frame {
	return &Frame{
		Method:  FrameMethodControl,
		Service: serviceID,
		Headers: []FrameHeader{{Key: FrameHeaderTypeKey, Value: FrameHeaderTypePing}},
	}
}

// NewPongFrame 构建对服务器主动 ping 的客户端响应。飞书可能在任何节奏下推
// ping，我们同款回应即可。
func NewPongFrame(serviceID int32) *Frame {
	return &Frame{
		Method:  FrameMethodControl,
		Service: serviceID,
		Headers: []FrameHeader{{Key: FrameHeaderTypeKey, Value: FrameHeaderTypePong}},
	}
}

// NewAckFrame 构建对一条入站数据帧的 ACK 响应。按 SDK 惯例，ACK 原样复用
// 入站帧的 Headers（服务器可据此用 message_id 关联），并在 Payload 里写一个
// JSON 编码的 Response 结构。
//
// codeOK 为 true 表示分发成功（Response.code=200）；false 则向服务器暴露 500
// （它会重试该事件）。payload 形态镜像 SDK 的 NewResponseByCode JSON：nil 的
// Headers map 与 nil 的 Data slice 在 stdlib encoding/json 里都会序列化成
// JSON null，这正是服务器期望收到的。
func NewAckFrame(inbound *Frame, codeOK bool) *Frame {
	code := 200
	if !codeOK {
		code = 500
	}
	payload := fmt.Sprintf(`{"code":%d,"headers":null,"data":null}`, code)
	return &Frame{
		Method:  inbound.Method,
		Service: inbound.Service,
		Headers: inbound.Headers,
		Payload: []byte(payload),
	}
}
