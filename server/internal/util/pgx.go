package util

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ParseUUID 把字符串解析为 pgtype.UUID。非法输入返回错误而不是零值 UUID——
// 静默丢弃坏输入曾导致数据丢失类 bug（例如 DELETE 没匹配到行却返回 204 成功）。
//
// 在用户输入边界（URL 参数、请求体、请求头）都请用它，出错时配合 4xx 响应。
// 对可信、已校验的 UUID 字符串（sqlc round-trip、测试夹具），用 MustParseUUID。
func ParseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, fmt.Errorf("invalid UUID %q: %w", s, err)
	}
	if !u.Valid {
		return u, fmt.Errorf("invalid UUID: %q", s)
	}
	return u, nil
}

// MustParseUUID 把字符串解析为 pgtype.UUID，非法输入直接 panic。
// 只给可信调用方用（已校验的 round-trip、测试夹具）。请求边界请用 ParseUUID 并回 4xx。
func MustParseUUID(s string) pgtype.UUID {
	u, err := ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// UUIDToString 把 pgtype.UUID 格式化成标准 "xxxxxxxx-xxxx-..." 字符串；无效值返回空串。
func UUIDToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// TextToPtr 把可空 pgtype.Text 转为 *string（无效为 nil）。
func TextToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// PtrToText 把 *string 转为可空 pgtype.Text（nil 为无效）。
func PtrToText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// StrToText 把非空字符串转为有效的 pgtype.Text；空串转为无效值（等价 NULL）。
func StrToText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// TimestampToString 把可空时间戳格式化成 RFC3339；无效返回空串。
func TimestampToString(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format(time.RFC3339)
}

// TimestampToPtr 把可空时间戳格式化成 RFC3339 指针；无效返回 nil。
func TimestampToPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format(time.RFC3339)
	return &s
}

// TimestampToNanoPtr 保留 PostgreSQL 的亚秒精度。新的排序与游标字段应该用它而不是
// 旧版秒级精度的 TimestampToPtr，这样同一秒内的两条活动仍可区分。
func TimestampToNanoPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format(time.RFC3339Nano)
	return &s
}

// DateToPtr 把 pgtype.Date 格式化为仅日期的 "YYYY-MM-DD" 字符串，未设置时为 nil。
// issue 的 start_date/due_date 是"不含时刻与时区的日历日"，绝不能当作某个时间点来渲染。
func DateToPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format(time.DateOnly)
	return &s
}

// ParseCalendarDate 从 "YYYY-MM-DD" 字符串解析出"无时刻、无时区"的 pgtype.Date 日历日。
//
// 为向后兼容，它也接受 RFC3339 时间戳，但仅当它恰好落在 UTC 某天的零点边界上
// （如 "2026-03-01T00:00:00Z"），此时能无歧义地代表那个日历日。非零点时刻的
// 时间戳是遗留的"本地午夜按 UTC 存"的值（例如 UTC+8 对所选日 2026-03-01 会发
// "2026-02-28T16:00:00Z"），其意图的日历日已不可恢复——这里大声拒绝，而不是悄悄
// 存成错误的日期。新客户端一律发 "YYYY-MM-DD"。
func ParseCalendarDate(s string) (pgtype.Date, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return pgtype.Date{Time: t, Valid: true}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		u := t.UTC()
		if u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0 {
			return pgtype.Date{
				Time:  time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC),
				Valid: true,
			}, nil
		}
		return pgtype.Date{}, fmt.Errorf("invalid date %q: timestamps must be a UTC midnight boundary (e.g. 2026-03-01T00:00:00Z); use YYYY-MM-DD", s)
	}
	return pgtype.Date{}, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", s)
}

// UUIDToPtr 把可空 UUID 转成标准字符串指针；无效返回 nil。
func UUIDToPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := UUIDToString(u)
	return &s
}

// Int8ToPtr 把可空 int8 转成 *int64；无效返回 nil。
func Int8ToPtr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// Int4ToPtr 把可空 int4 转成 *int32；无效返回 nil。
func Int4ToPtr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}

// PtrToInt4 把 *int32 转成可空 pgtype.Int4（nil 为无效）。
func PtrToInt4(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}
